cmake_minimum_required(VERSION 3.16)
project(rwkv_server CXX)

add_executable(rwkv_server src/main.cpp)

target_include_directories(rwkv_server PRIVATE
    ${httplib_SOURCE_DIR}
    ${json_SOURCE_DIR}/single_include
    ${CMAKE_SOURCE_DIR}/third_party/rwkv.cpp
    ${CMAKE_SOURCE_DIR}/third_party/rwkv.cpp/ggml/include
)

target_link_libraries(rwkv_server PRIVATE rwkv)

if(CMAKE_SYSTEM_PROCESSOR MATCHES "x86_64|AMD64")
    target_compile_options(rwkv_server PRIVATE -mavx2 -mfma -O3 -DNDEBUG)
elseif(CMAKE_SYSTEM_PROCESSOR MATCHES "arm64|aarch64")
    target_compile_options(rwkv_server PRIVATE -march=armv8-a+fp+simd -O3 -DNDEBUG)
else()
    target_compile_options(rwkv_server PRIVATE -O3 -DNDEBUG)
endif()

target_compile_options(rwkv_server PRIVATE -Wall -Wextra)
set_target_properties(rwkv_server PROPERTIES RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/cpp)
