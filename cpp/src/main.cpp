#include <httplib.h>
#include <nlohmann/json.hpp>
#include <rwkv.h>

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <iostream>
#include <map>
#include <random>
#include <sstream>
#include <string>
#include <unordered_map>
#include <vector>

using json = nlohmann::json;

// ---------------------------------------------------------------------------
// WorldTokenizer: Minimal C++ port of RWKV World tokenizer.
// Reads rwkv_vocab_v20230424.txt and builds greedy longest-match encoding.
// ---------------------------------------------------------------------------
class WorldTokenizer {
   public:
    bool load(const std::string& vocab_path) {
        std::ifstream f(vocab_path);
        if (!f.is_open()) {
            std::cerr << "[Tokenizer] Failed to open " << vocab_path << std::endl;
            return false;
        }

        std::string line;
        while (std::getline(f, line)) {
            if (line.empty()) continue;
            // Format: index literal length
            // Example: 34550 'hello' 5
            // Example: 130 b'\x80' 1
            size_t first_space = line.find(' ');
            if (first_space == std::string::npos) continue;

            size_t last_space = line.find_last_of(' ');
            if (last_space == std::string::npos || last_space <= first_space) continue;

            std::string literal_part = line.substr(first_space + 1, last_space - first_space - 1);
            if (literal_part.empty()) continue;

            // Strip optional leading 'b' for bytes literals
            size_t off = 0;
            if (literal_part.size() > 1 && literal_part[0] == 'b' && literal_part[1] == '\'') {
                off = 1;
            }
            if (literal_part.size() < off + 2 || literal_part[off] != '\'' || literal_part.back() != '\'') continue;

            std::string inner = literal_part.substr(off + 1, literal_part.size() - off - 2);
            std::string bytes = unescape(inner);

            uint32_t idx = static_cast<uint32_t>(std::stoul(line.substr(0, first_space)));
            index_to_token_[idx] = bytes;
            token_to_index_[bytes] = idx;

            if (!bytes.empty()) {
                first_byte_map_[static_cast<uint8_t>(bytes[0])].push_back({bytes, idx});
            }
        }

        std::cerr << "[Tokenizer] Loaded " << index_to_token_.size() << " tokens." << std::endl;
        return !index_to_token_.empty();
    }

    std::vector<uint32_t> encode(const std::string& text) const {
        std::vector<uint32_t> tokens;
        const uint8_t* data = reinterpret_cast<const uint8_t*>(text.data());
        size_t len = text.size();
        size_t i = 0;

        while (i < len) {
            size_t best_len = 0;
            uint32_t best_token = 0;

            auto it = first_byte_map_.find(data[i]);
            if (it != first_byte_map_.end()) {
                for (const auto& entry : it->second) {
                    const std::string& tok_bytes = entry.first;
                    if (tok_bytes.size() > best_len && i + tok_bytes.size() <= len) {
                        if (std::memcmp(data + i, tok_bytes.data(), tok_bytes.size()) == 0) {
                            best_len = tok_bytes.size();
                            best_token = entry.second;
                        }
                    }
                }
            }

            if (best_len == 0) {
                best_len = 1;
                auto fb = token_to_index_.find(std::string(1, static_cast<char>(data[i])));
                if (fb != token_to_index_.end()) {
                    best_token = fb->second;
                } else {
                    best_token = 0;
                }
            }

            tokens.push_back(best_token);
            i += best_len;
        }
        return tokens;
    }

    std::string decode(const std::vector<uint32_t>& tokens) const {
        std::string result;
        for (uint32_t tok : tokens) {
            auto it = index_to_token_.find(tok);
            if (it != index_to_token_.end()) {
                result += it->second;
            }
        }
        return result;
    }

   private:
    std::map<uint32_t, std::string> index_to_token_;
    std::map<std::string, uint32_t> token_to_index_;
    std::unordered_map<uint8_t, std::vector<std::pair<std::string, uint32_t>>> first_byte_map_;

    static std::string unescape(const std::string& s) {
        std::string out;
        for (size_t i = 0; i < s.size(); ++i) {
            if (s[i] == '\\' && i + 1 < s.size()) {
                char c = s[i + 1];
                if (c == '\\') {
                    out += '\\';
                } else if (c == '\'') {
                    out += '\'';
                } else if (c == 'n') {
                    out += '\n';
                } else if (c == 't') {
                    out += '\t';
                } else if (c == 'r') {
                    out += '\r';
                } else if (c == 'x' && i + 3 < s.size()) {
                    int val = std::stoi(s.substr(i + 2, 2), nullptr, 16);
                    out += static_cast<char>(val);
                    i += 2;
                } else {
                    out += c;
                }
                ++i;
            } else {
                out += s[i];
            }
        }
        return out;
    }
};

// ---------------------------------------------------------------------------
// Sampling
// ---------------------------------------------------------------------------
static uint32_t sample_token(const float* logits, size_t n_vocab, float temperature) {
    if (temperature <= 0.0f) {
        return static_cast<uint32_t>(std::max_element(logits, logits + n_vocab) - logits);
    }

    std::vector<float> probs(logits, logits + n_vocab);
    float max_logit = *std::max_element(probs.begin(), probs.end());
    float sum = 0.0f;
    for (size_t i = 0; i < n_vocab; ++i) {
        probs[i] = std::exp((probs[i] - max_logit) / temperature);
        sum += probs[i];
    }

    static thread_local std::mt19937 gen(std::random_device{}());
    std::uniform_real_distribution<float> dist(0.0f, 1.0f);
    float r = dist(gen) * sum;

    for (size_t i = 0; i < n_vocab; ++i) {
        r -= probs[i];
        if (r <= 0.0f) return static_cast<uint32_t>(i);
    }
    return static_cast<uint32_t>(n_vocab - 1);
}

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------
static rwkv_context* g_ctx = nullptr;
static WorldTokenizer g_tokenizer;
static const size_t MAX_NEW_TOKENS = 512;
static const float TEMPERATURE = 0.3f;

// ---------------------------------------------------------------------------
// Inference helper
// ---------------------------------------------------------------------------
static bool ends_with_stop(const std::string& text, const std::string& stop) {
    if (text.size() < stop.size()) return false;
    return text.compare(text.size() - stop.size(), stop.size(), stop) == 0;
}

static std::string run_inference(const std::string& prompt) {
    auto tokens = g_tokenizer.encode(prompt);
    if (tokens.empty()) return "";

    size_t state_len = rwkv_get_state_len(g_ctx);
    size_t vocab_size = rwkv_get_n_vocab(g_ctx);

    std::vector<float> state(state_len);
    std::vector<float> logits(vocab_size);
    rwkv_init_state(g_ctx, state.data());

    // Evaluate prompt
    if (!tokens.empty()) {
        rwkv_eval_sequence_in_chunks(g_ctx, tokens.data(), tokens.size(), 16,
                                     nullptr, state.data(), logits.data());
    }

    std::vector<uint32_t> response_tokens;
    const std::vector<std::string> stop_seqs = {
        "\n\nUser:", "\n\nAssistant:",
        "\nUser:", "\nAssistant:",
        "User:", "Assistant:"
    };

    for (size_t i = 0; i < MAX_NEW_TOKENS; ++i) {
        uint32_t next = sample_token(logits.data(), vocab_size, TEMPERATURE);
        if (next == 0) break;
        response_tokens.push_back(next);
        rwkv_eval(g_ctx, next, state.data(), state.data(), logits.data());

        // Early stop on natural turn boundaries
        std::string partial = g_tokenizer.decode(response_tokens);
        for (const auto& stop : stop_seqs) {
            if (ends_with_stop(partial, stop)) {
                return partial.substr(0, partial.size() - stop.size());
            }
            auto pos = partial.find(stop);
            if (pos != std::string::npos) {
                return partial.substr(0, pos);
            }
        }
    }

    return g_tokenizer.decode(response_tokens);
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------
static json handle_chat_completion(const json& req) {
    std::string prompt;
    std::string system_text;
    bool has_assistant_turn = false;
    if (req.contains("messages") && req["messages"].is_array()) {
        for (const auto& m : req["messages"]) {
            std::string role = m.value("role", "");
            std::string content = m.value("content", "");
            if (role == "system") {
                if (!system_text.empty()) system_text += "\n";
                system_text += content;
            } else if (role == "user") {
                prompt += "User: " + content + "\n\n";
            } else if (role == "assistant") {
                prompt += "Assistant: " + content + "\n\n";
                has_assistant_turn = true;
            } else if (role == "tool") {
                prompt += "User: [Tool result] " + content + "\n\n";
            }
        }
    }
    // RWKV World models work best with the standard chat format.
    // Prepend system text as an initial assistant turn rather than using
    // an "Instruction:" prefix (World models are not trained on that format).
    if (!system_text.empty()) {
        prompt = "Assistant: " + system_text + "\n\n" + prompt;
    }
    // Prime the model with a sample assistant introduction when there is no
    // prior assistant turn. This follows the official RWKV chat examples and
    // prevents the model from echoing the user's question.
    if (!has_assistant_turn) {
        prompt = "User: hi\n\nAssistant: Hello! I am your assistant. How can I help you today?\n\n" + prompt;
    }
    prompt += "Assistant:";

    std::string reply = run_inference(prompt);

    json response;
    response["choices"] = json::array();
    json choice;
    choice["message"] = {{"role", "assistant"}, {"content", reply}};
    choice["finish_reason"] = "stop";
    choice["index"] = 0;
    response["choices"].push_back(choice);
    response["model"] = req.value("model", "bitnet-rwkv");
    return response;
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------
int main(int argc, char** argv) {
    const char* model_path = "model.bin";
    if (argc > 1) {
        model_path = argv[1];
    } else {
        const char* env_path = std::getenv("RWKV_MODEL_PATH");
        if (env_path) model_path = env_path;
    }
    const char* vocab_path = (argc > 2) ? argv[2] : "third_party/rwkv.cpp/python/rwkv_cpp/rwkv_vocab_v20230424.txt";

    std::cerr << "[RWKV Server] Loading model from " << model_path << std::endl;
    uint32_t n_gpu_layers = 99;
    const char* env_ngl = std::getenv("RWKV_N_GPU_LAYERS");
    if (env_ngl) {
        n_gpu_layers = static_cast<uint32_t>(std::atoi(env_ngl));
    }
    uint32_t n_threads = (n_gpu_layers > 0) ? 1 : 4;
    const char* env_threads = std::getenv("RWKV_N_THREADS");
    if (env_threads) {
        n_threads = static_cast<uint32_t>(std::atoi(env_threads));
    }
    std::cerr << "[RWKV Server] Threads=" << n_threads << " GPU layers=" << n_gpu_layers << std::endl;
    g_ctx = rwkv_init_from_file(model_path, n_threads, n_gpu_layers);
    if (!g_ctx) {
        std::cerr << "[RWKV Server] FATAL: Failed to load model. Exit code 1." << std::endl;
        return 1;
    }
    std::cerr << "[RWKV Server] Model loaded. Vocab=" << rwkv_get_n_vocab(g_ctx)
              << " StateLen=" << rwkv_get_state_len(g_ctx) << std::endl;

    std::cerr << "[RWKV Server] Loading tokenizer from " << vocab_path << std::endl;
    if (!g_tokenizer.load(vocab_path)) {
        std::cerr << "[RWKV Server] FATAL: Failed to load tokenizer. Exit code 1." << std::endl;
        rwkv_free(g_ctx);
        return 1;
    }

    httplib::Server svr;

    svr.Get("/health", [](const httplib::Request&, httplib::Response& res) {
        res.set_content("{\"status\":\"ok\"}", "application/json");
    });

    svr.Post("/v1/chat/completions", [](const httplib::Request& req, httplib::Response& res) {
        try {
            auto j = json::parse(req.body);
            auto resp = handle_chat_completion(j);
            res.set_content(resp.dump(), "application/json");
        } catch (const std::exception& e) {
            res.status = 400;
            res.set_content(std::string("{\"error\":\"") + e.what() + "\"}", "application/json");
        }
    });

    std::cout << "[RWKV Server] Listening on 0.0.0.0:9090" << std::endl;
    if (!svr.listen("0.0.0.0", 9090)) {
        std::cerr << "[RWKV Server] Failed to bind port 9090" << std::endl;
        rwkv_free(g_ctx);
        return 1;
    }

    rwkv_free(g_ctx);
    return 0;
}
