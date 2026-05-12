#pragma once
#include <string>
#include <vector>

// String operations
int         str_length(const std::string& s);
std::string str_copy(const std::string& s);
std::string str_concat(const std::string& a, const std::string& b);
int         str_compare(const std::string& a, const std::string& b);
std::string str_reverse(const std::string& s);
std::string str_upper(const std::string& s);
std::string str_lower(const std::string& s);
std::string str_trim(const std::string& s);
int         str_find(const std::string& s, const std::string& sub);
std::string str_replace(const std::string& s, const std::string& from, const std::string& to);
int         str_split_count(const std::string& s, char sep);
int         str_to_int(const std::string& s);
std::string str_from_int(int n);
std::string str_repeat(const std::string& s, int n);
std::string str_pad_left(const std::string& s, int width, char c);
std::string str_pad_right(const std::string& s, int width, char c);
bool        str_starts_with(const std::string& s, const std::string& prefix);
bool        str_ends_with(const std::string& s, const std::string& suffix);
bool        str_contains(const std::string& s, const std::string& sub);
int         str_count_char(const std::string& s, char c);
std::string str_strip_prefix(const std::string& s, const std::string& prefix);
std::string str_strip_suffix(const std::string& s, const std::string& suffix);
bool        str_is_empty(const std::string& s);
bool        str_is_palindrome(const std::string& s);
std::string str_rotate(const std::string& s, int n);

// Math operations
int    math_add(int a, int b);
int    math_sub(int a, int b);
int    math_mul(int a, int b);
int    math_div(int a, int b);
int    math_mod(int a, int b);
double math_pow(double base, int exp);
double math_sqrt(double x);
int    math_abs(int x);
int    math_min(int a, int b);
int    math_max(int a, int b);
int    math_gcd(int a, int b);
int    math_lcm(int a, int b);
bool   math_is_prime(int n);
long   math_factorial(int n);
long   math_fibonacci(int n);
int    math_clamp(int x, int lo, int hi);
double math_lerp(double a, double b, double t);
int    math_round(double x);
int    math_floor(double x);
int    math_ceil(double x);
int    math_sign(int x);
double math_average(const std::vector<int>& v);
int    math_sum_array(const std::vector<int>& v);
int    math_product_array(const std::vector<int>& v);
bool   math_is_even(int n);

// Array operations
void arr_fill(std::vector<int>& v, int val);
std::vector<int> arr_copy(const std::vector<int>& v);
int  arr_find(const std::vector<int>& v, int val);
bool arr_contains(const std::vector<int>& v, int val);
int  arr_count(const std::vector<int>& v, int val);
int  arr_sum(const std::vector<int>& v);
int  arr_min(const std::vector<int>& v);
int  arr_max(const std::vector<int>& v);
void arr_reverse(std::vector<int>& v);
void arr_sort_asc(std::vector<int>& v);
void arr_sort_desc(std::vector<int>& v);
void arr_rotate(std::vector<int>& v, int k);
int  arr_unique_count(const std::vector<int>& v);
void arr_swap(std::vector<int>& v, int i, int j);
void arr_shift_left(std::vector<int>& v, int k);
void arr_shift_right(std::vector<int>& v, int k);
bool arr_is_sorted(const std::vector<int>& v);
int  arr_binary_search(const std::vector<int>& v, int val);
std::vector<int> arr_prefix_sum(const std::vector<int>& v);
int  arr_max_subarray(const std::vector<int>& v);
std::vector<int> arr_flatten_2d(const std::vector<std::vector<int>>& vv);
int  arr_dot_product(const std::vector<int>& a, const std::vector<int>& b);
void arr_scale(std::vector<int>& v, int factor);
void arr_clamp_all(std::vector<int>& v, int lo, int hi);
int  arr_count_if(const std::vector<int>& v, int threshold);

// Utility operations
int  util_clamp_int(int x, int lo, int hi);
void util_swap_int(int& a, int& b);
bool util_is_digit(char c);
bool util_is_alpha(char c);
bool util_is_space(char c);
char util_to_upper_char(char c);
char util_to_lower_char(char c);
unsigned util_hash_simple(const std::string& s);
int  util_count_bits(unsigned x);
unsigned util_reverse_bits(unsigned x);
int  util_popcount(unsigned x);
int  util_log2_int(unsigned x);
unsigned util_next_power2(unsigned x);
unsigned util_align_up(unsigned x, unsigned align);
unsigned util_align_down(unsigned x, unsigned align);
bool util_in_range(int x, int lo, int hi);
double util_clamp_double(double x, double lo, double hi);
double util_lerp_double(double a, double b, double t);
bool util_parse_bool(const std::string& s);
long util_timestamp_ms();
std::string util_format_size(long bytes);
void util_sleep_ms(int ms);
unsigned util_xorshift32(unsigned x);
unsigned util_rotate_left32(unsigned x, int n);
unsigned util_rotate_right32(unsigned x, int n);
