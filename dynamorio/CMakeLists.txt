# CMakeLists.txt
#
# This file tells CMake how to build our DynamoRIO client and the test program.

cmake_minimum_required(VERSION 3.10)

# Define both C++ and C as project languages at the top level.
project(CoverageTool CXX C)

# Find the DynamoRIO package. You must provide the path to it
# when you run cmake (e.g., -DDynamoRIO_DIR=...).
find_package(DynamoRIO REQUIRED)

# Use global commands to ensure include paths and definitions are found.
include_directories(${DynamoRIO_INCLUDE_DIRS})
# FIX: Explicitly add the include directory for DynamoRIO extensions (drmgr, drsyms, etc.)
include_directories(${DynamoRIO_INCLUDE_DIRS}/../ext/include)

add_definitions(${DynamoRIO_DEFINITIONS})

# --- drcov Client ---
# Add our client source file as a shared library.
add_library(drcov SHARED drcov.cpp)

# Automatically add the required definitions based on the detected OS and architecture.
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
    target_compile_definitions(drcov PRIVATE LINUX)
elseif(CMAKE_SYSTEM_NAME STREQUAL "Darwin") # macOS
    target_compile_definitions(drcov PRIVATE MACOS)
elseif(CMAKE_SYSTEM_NAME STREQUAL "Windows")
    target_compile_definitions(drcov PRIVATE WINDOWS)
endif()

if(CMAKE_SYSTEM_PROCESSOR STREQUAL "x86_64")
    target_compile_definitions(drcov PRIVATE X86_64)
elseif(CMAKE_SYSTEM_PROCESSOR MATCHES "aarch64|arm64")
    target_compile_definitions(drcov PRIVATE ARM_64)
endif()

# Add optimization flags to the client library for better performance.
target_compile_options(drcov PRIVATE -O2)

# Configure the output name to be libdrcov.so (or .dll/.dylib).
set_target_properties(drcov PROPERTIES
    PREFIX ""
    OUTPUT_NAME "libdrcov"
)

# Link our client against the DynamoRIO libraries.
target_link_libraries(drcov PRIVATE ${DynamoRIO_LIBRARIES})

# FIX: Statically link the C++ standard library into our client.
# This is the most robust way to solve the "undefined symbol" runtime error.
target_link_options(drcov PRIVATE -static-libstdc++)

# --- Test Program ---
# This part is just for convenience to build our test target.
add_executable(test_program test_program.c)
# Add the -g flag to compile the test program with debugging symbols.
target_compile_options(test_program PRIVATE -g)
