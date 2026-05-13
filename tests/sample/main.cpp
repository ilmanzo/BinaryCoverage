#include "functions.h"
#include <cstring>
#include <iostream>

static void run_strings() {
    std::string s = "Hello World";
    str_length(s);
    str_copy(s);
    str_concat(s, "!");
    str_compare(s, "Hello");
    str_reverse(s);
    str_upper(s);
    str_lower(s);
    str_trim("  hi  ");
    str_find(s, "World");
    str_replace(s, "World", "Earth");
    str_split_count("a,b,c", ',');
    str_to_int("42");
    str_from_int(99);
    str_repeat("ab", 3);
    str_pad_left("x", 5, '-');
    str_pad_right("x", 5, '-');
    str_starts_with(s, "Hello");
    str_ends_with(s, "World");
    str_contains(s, "lo");
    str_count_char(s, 'l');
    str_strip_prefix(s, "Hello ");
    str_strip_suffix(s, " World");
    str_is_empty(s);
    str_is_palindrome("racecar");
    str_rotate(s, 3);
}

static void run_math() {
    std::vector<int> v = {1,2,3,4,5};
    math_add(3, 4);
    math_sub(10, 3);
    math_mul(6, 7);
    math_div(15, 3);
    math_mod(17, 5);
    math_pow(2.0, 8);
    math_sqrt(144.0);
    math_abs(-42);
    math_min(3, 7);
    math_max(3, 7);
    math_gcd(48, 18);
    math_lcm(4, 6);
    math_is_prime(17);
    math_factorial(10);
    math_fibonacci(20);
    math_clamp(15, 0, 10);
    math_lerp(0.0, 1.0, 0.5);
    math_round(3.6);
    math_floor(3.9);
    math_ceil(3.1);
    math_sign(-5);
    math_average(v);
    math_sum_array(v);
    math_product_array(v);
    math_is_even(4);
}

static void run_arrays() {
    std::vector<int> v = {5,3,1,4,2};
    arr_fill(v, 0);
    arr_copy(v);
    arr_find(v, 3);
    arr_contains(v, 4);
    arr_count(v, 1);
    arr_sum(v);
    std::vector<int> w = {1,2,3,4,5};
    arr_min(w);
    arr_max(w);
    arr_reverse(v);
    arr_sort_asc(v);
    arr_sort_desc(v);
    arr_rotate(v, 2);
    arr_unique_count(v);
    arr_swap(v, 0, 1);
    arr_shift_left(v, 1);
    arr_shift_right(v, 1);
    arr_is_sorted(w);
    arr_binary_search(w, 3);
    arr_prefix_sum(w);
    arr_max_subarray(w);
    std::vector<std::vector<int>> m = {{1,2},{3,4}};
    arr_flatten_2d(m);
    arr_dot_product(w, w);
    arr_scale(v, 2);
    arr_clamp_all(v, 0, 10);
    arr_count_if(w, 2);
}

static void run_utils() {
    int a = 3, b = 7;
    util_clamp_int(5, 1, 10);
    util_swap_int(a, b);
    util_is_digit('5');
    util_is_alpha('z');
    util_is_space(' ');
    util_to_upper_char('a');
    util_to_lower_char('A');
    util_hash_simple("hello");
    util_count_bits(255);
    util_reverse_bits(0x12345678);
    util_popcount(0xFF);
    util_log2_int(64);
    util_next_power2(100);
    util_align_up(13, 8);
    util_align_down(13, 8);
    util_in_range(5, 1, 10);
    util_clamp_double(1.5, 0.0, 1.0);
    util_lerp_double(0.0, 10.0, 0.5);
    util_parse_bool("true");
    util_timestamp_ms();
    util_format_size(1024*1024);
    util_sleep_ms(1);
    util_xorshift32(12345);
    util_rotate_left32(1, 8);
    util_rotate_right32(256, 8);
}

int main(int argc, char* argv[]) {
    bool do_strings = false, do_math = false, do_arrays = false, do_utils = false;
    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--strings") == 0) do_strings = true;
        else if (strcmp(argv[i], "--math")    == 0) do_math    = true;
        else if (strcmp(argv[i], "--arrays")  == 0) do_arrays  = true;
        else if (strcmp(argv[i], "--utils")   == 0) do_utils   = true;
        else if (strcmp(argv[i], "--all")     == 0) {
            do_strings = do_math = do_arrays = do_utils = true;
        }
    }
    if (!do_strings && !do_math && !do_arrays && !do_utils) {
        std::cerr << "Usage: " << argv[0]
                  << " [--strings] [--math] [--arrays] [--utils] [--all]\n";
        return 1;
    }
    if (do_strings) { run_strings(); std::cout << "strings: ok\n"; }
    if (do_math)    { run_math();    std::cout << "math: ok\n"; }
    if (do_arrays)  { run_arrays();  std::cout << "arrays: ok\n"; }
    if (do_utils)   { run_utils();   std::cout << "utils: ok\n"; }
    return 0;
}
