#define CATCH_CONFIG_MAIN
#include "catch2/catch.hpp"
#include "../FuncTracer.hpp"

TEST_CASE("func_is_relevant works as expected") {
    SECTION("PLT functions are not relevant") {
        REQUIRE_FALSE(func_is_relevant("foo@plt"));
        REQUIRE_FALSE(func_is_relevant("bar@plt"));
    }
    SECTION("Functions starting with __ are not relevant") {
        REQUIRE_FALSE(func_is_relevant("__internal"));
        REQUIRE_FALSE(func_is_relevant("__something"));
    }
    SECTION("Explicit blacklist") {
        REQUIRE_FALSE(func_is_relevant("main"));
        REQUIRE_FALSE(func_is_relevant("_init"));
        REQUIRE_FALSE(func_is_relevant("_start"));
        REQUIRE_FALSE(func_is_relevant(".plt.got"));
    }
    SECTION("Normal functions are relevant") {
        REQUIRE(func_is_relevant("foo"));
        REQUIRE(func_is_relevant("bar"));
        REQUIRE(func_is_relevant("baz"));
    }
    SECTION("Short names are relevant") {
        REQUIRE(func_is_relevant("a"));
        REQUIRE(func_is_relevant("b@p"));
        REQUIRE(func_is_relevant("_m"));
    }
}

TEST_CASE("image_is_relevant works as expected") {
    SECTION("Blacklisted images are not relevant") {
        REQUIRE_FALSE(image_is_relevant("[vdso]"));
    }
    SECTION("Normal images are relevant") {
        REQUIRE(image_is_relevant("/usr/bin/ls"));
        REQUIRE(image_is_relevant("libc.so.6"));
        REQUIRE(image_is_relevant("mybinary"));
    }
}