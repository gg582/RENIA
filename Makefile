.PHONY: all clean run deps model model-check templ cpp go

RWKV_REPO := https://github.com/saharNooby/rwkv.cpp
RWKV_DIR := third_party/rwkv.cpp
CPP_BUILD_DIR := build
GO_BINARY := renia
CPP_BINARY := $(CPP_BUILD_DIR)/cpp/rwkv_server

# Order of Execution: deps -> cpp -> templ -> go -> execution
all: deps cpp templ go

deps:
	@echo "==> Checking RWKV C++ core dependency..."
	@if [ ! -d "$(RWKV_DIR)/.git" ]; then \
		mkdir -p third_party && \
		git clone --recursive --depth 1 $(RWKV_REPO) $(RWKV_DIR); \
	else \
		echo "RWKV core already present."; \
	fi

model:
	@echo "==> Installing recommended RWKV-6 World 7B model (Q4_0)..."
	python3 scripts/setup_rwkv_model.py --model 6b7 --quant Q4_0

model-check:
	@if [ ! -f "model.bin" ]; then \
		echo "ERROR: model.bin not found."; \
		echo "Run one of the following to download a modern model:"; \
		echo "  make model                                      # RWKV-6 World 7B Q4_0 (recommended for 8GB VRAM)"; \
		echo "  python3 scripts/setup_rwkv_model.py --model 7b3 --quant Q5_1  # RWKV-7 World 2.9B Q5_1 (latest)"; \
		exit 1; \
	fi

$(CPP_BINARY): deps
	@echo "==> Building C++ inference server..."
	@mkdir -p $(CPP_BUILD_DIR)
	cd $(CPP_BUILD_DIR) && cmake .. -DCMAKE_BUILD_TYPE=Release
	cd $(CPP_BUILD_DIR) && cmake --build . --parallel --target rwkv_server
	@ln -sf third_party/rwkv.cpp/librwkv.so $(CPP_BUILD_DIR)/librwkv.so

cpp: $(CPP_BINARY)

templ:
	@echo "==> Generating templ files..."
	@which templ >/dev/null 2>&1 || go install github.com/a-h/templ/cmd/templ@latest
	templ generate

go: templ
	@echo "==> Building Go backend..."
	CGO_ENABLED=0 GOGC=20 go build -ldflags="-s -w" -trimpath -o $(GO_BINARY) .

clean:
	rm -f $(GO_BINARY)
	rm -rf $(CPP_BUILD_DIR)
	rm -f *_templ.go
	go clean -cache
	@echo "==> Removing old model.bin (run 'make model' or python3 scripts/setup_rwkv_model.py to fetch a fresh one)"
	rm -f model.bin
	rm -rf models/*.bin

db-clean:
	@echo "==> Wiping SQLite database..."
	rm -f renia.db
	rm -f renia.db-shm
	rm -f renia.db-wal

run: all model-check
	./$(GO_BINARY)
