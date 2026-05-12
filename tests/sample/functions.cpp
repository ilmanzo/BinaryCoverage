#include "functions.h"
#include <algorithm>
#include <cctype>
#include <chrono>
#include <cmath>
#include <numeric>
#include <set>
#include <sstream>
#include <thread>

// --- String operations ---

int str_length(const std::string& s) { return (int)s.size(); }
std::string str_copy(const std::string& s) { return s; }
std::string str_concat(const std::string& a, const std::string& b) { return a + b; }
int str_compare(const std::string& a, const std::string& b) { return a.compare(b); }

std::string str_reverse(const std::string& s) {
    std::string r = s; std::reverse(r.begin(), r.end()); return r;
}
std::string str_upper(const std::string& s) {
    std::string r = s; for (auto& c : r) c = toupper(c); return r;
}
std::string str_lower(const std::string& s) {
    std::string r = s; for (auto& c : r) c = tolower(c); return r;
}
std::string str_trim(const std::string& s) {
    auto b = s.find_first_not_of(" \t\n\r");
    auto e = s.find_last_not_of(" \t\n\r");
    return (b == std::string::npos) ? "" : s.substr(b, e - b + 1);
}
int str_find(const std::string& s, const std::string& sub) {
    auto pos = s.find(sub);
    return (pos == std::string::npos) ? -1 : (int)pos;
}
std::string str_replace(const std::string& s, const std::string& from, const std::string& to) {
    std::string r = s;
    size_t pos = 0;
    while ((pos = r.find(from, pos)) != std::string::npos) {
        r.replace(pos, from.size(), to);
        pos += to.size();
    }
    return r;
}
int str_split_count(const std::string& s, char sep) {
    return 1 + (int)std::count(s.begin(), s.end(), sep);
}
int str_to_int(const std::string& s) { return std::stoi(s); }
std::string str_from_int(int n) { return std::to_string(n); }
std::string str_repeat(const std::string& s, int n) {
    std::string r; for (int i = 0; i < n; i++) r += s; return r;
}
std::string str_pad_left(const std::string& s, int width, char c) {
    if ((int)s.size() >= width) return s;
    return std::string(width - s.size(), c) + s;
}
std::string str_pad_right(const std::string& s, int width, char c) {
    if ((int)s.size() >= width) return s;
    return s + std::string(width - s.size(), c);
}
bool str_starts_with(const std::string& s, const std::string& prefix) {
    return s.size() >= prefix.size() && s.compare(0, prefix.size(), prefix) == 0;
}
bool str_ends_with(const std::string& s, const std::string& suffix) {
    return s.size() >= suffix.size() && s.compare(s.size() - suffix.size(), suffix.size(), suffix) == 0;
}
bool str_contains(const std::string& s, const std::string& sub) { return s.find(sub) != std::string::npos; }
int str_count_char(const std::string& s, char c) { return (int)std::count(s.begin(), s.end(), c); }
std::string str_strip_prefix(const std::string& s, const std::string& prefix) {
    return str_starts_with(s, prefix) ? s.substr(prefix.size()) : s;
}
std::string str_strip_suffix(const std::string& s, const std::string& suffix) {
    return str_ends_with(s, suffix) ? s.substr(0, s.size() - suffix.size()) : s;
}
bool str_is_empty(const std::string& s) { return s.empty(); }
bool str_is_palindrome(const std::string& s) { return s == str_reverse(s); }
std::string str_rotate(const std::string& s, int n) {
    if (s.empty()) return s;
    n = ((n % (int)s.size()) + s.size()) % s.size();
    return s.substr(n) + s.substr(0, n);
}

// --- Math operations ---

int    math_add(int a, int b) { return a + b; }
int    math_sub(int a, int b) { return a - b; }
int    math_mul(int a, int b) { return a * b; }
int    math_div(int a, int b) { return b != 0 ? a / b : 0; }
int    math_mod(int a, int b) { return b != 0 ? a % b : 0; }
double math_pow(double base, int exp) { return std::pow(base, exp); }
double math_sqrt(double x) { return std::sqrt(x); }
int    math_abs(int x) { return x < 0 ? -x : x; }
int    math_min(int a, int b) { return a < b ? a : b; }
int    math_max(int a, int b) { return a > b ? a : b; }
int    math_gcd(int a, int b) { return b == 0 ? a : math_gcd(b, a % b); }
int    math_lcm(int a, int b) { return a / math_gcd(a, b) * b; }
bool   math_is_prime(int n) {
    if (n < 2) return false;
    for (int i = 2; i * i <= n; i++) if (n % i == 0) return false;
    return true;
}
long math_factorial(int n) { long r = 1; for (int i = 2; i <= n; i++) r *= i; return r; }
long math_fibonacci(int n) {
    if (n <= 1) return n;
    long a = 0, b = 1;
    for (int i = 2; i <= n; i++) { long t = a + b; a = b; b = t; }
    return b;
}
int    math_clamp(int x, int lo, int hi) { return x < lo ? lo : x > hi ? hi : x; }
double math_lerp(double a, double b, double t) { return a + t * (b - a); }
int    math_round(double x) { return (int)std::round(x); }
int    math_floor(double x) { return (int)std::floor(x); }
int    math_ceil(double x)  { return (int)std::ceil(x); }
int    math_sign(int x) { return x > 0 ? 1 : x < 0 ? -1 : 0; }
double math_average(const std::vector<int>& v) {
    if (v.empty()) return 0.0;
    return (double)std::accumulate(v.begin(), v.end(), 0) / v.size();
}
int math_sum_array(const std::vector<int>& v) { return std::accumulate(v.begin(), v.end(), 0); }
int math_product_array(const std::vector<int>& v) { return std::accumulate(v.begin(), v.end(), 1, std::multiplies<int>()); }
bool math_is_even(int n) { return n % 2 == 0; }

// --- Array operations ---

void arr_fill(std::vector<int>& v, int val) { std::fill(v.begin(), v.end(), val); }
std::vector<int> arr_copy(const std::vector<int>& v) { return v; }
int  arr_find(const std::vector<int>& v, int val) {
    auto it = std::find(v.begin(), v.end(), val);
    return (it == v.end()) ? -1 : (int)(it - v.begin());
}
bool arr_contains(const std::vector<int>& v, int val) { return std::find(v.begin(), v.end(), val) != v.end(); }
int  arr_count(const std::vector<int>& v, int val) { return (int)std::count(v.begin(), v.end(), val); }
int  arr_sum(const std::vector<int>& v) { return std::accumulate(v.begin(), v.end(), 0); }
int  arr_min(const std::vector<int>& v) { return *std::min_element(v.begin(), v.end()); }
int  arr_max(const std::vector<int>& v) { return *std::max_element(v.begin(), v.end()); }
void arr_reverse(std::vector<int>& v) { std::reverse(v.begin(), v.end()); }
void arr_sort_asc(std::vector<int>& v) { std::sort(v.begin(), v.end()); }
void arr_sort_desc(std::vector<int>& v) { std::sort(v.begin(), v.end(), std::greater<int>()); }
void arr_rotate(std::vector<int>& v, int k) {
    if (v.empty()) return;
    k = ((k % (int)v.size()) + v.size()) % v.size();
    std::rotate(v.begin(), v.begin() + k, v.end());
}
int arr_unique_count(const std::vector<int>& v) { return (int)std::set<int>(v.begin(), v.end()).size(); }
void arr_swap(std::vector<int>& v, int i, int j) { std::swap(v[i], v[j]); }
void arr_shift_left(std::vector<int>& v, int k) {
    if (v.empty() || k <= 0) return;
    k = k % (int)v.size();
    std::rotate(v.begin(), v.begin() + k, v.end());
}
void arr_shift_right(std::vector<int>& v, int k) {
    if (v.empty() || k <= 0) return;
    k = k % (int)v.size();
    std::rotate(v.begin(), v.begin() + (v.size() - k), v.end());
}
bool arr_is_sorted(const std::vector<int>& v) { return std::is_sorted(v.begin(), v.end()); }
int  arr_binary_search(const std::vector<int>& v, int val) {
    auto it = std::lower_bound(v.begin(), v.end(), val);
    return (it != v.end() && *it == val) ? (int)(it - v.begin()) : -1;
}
std::vector<int> arr_prefix_sum(const std::vector<int>& v) {
    std::vector<int> r(v.size());
    std::partial_sum(v.begin(), v.end(), r.begin());
    return r;
}
int arr_max_subarray(const std::vector<int>& v) {
    int best = 0, cur = 0;
    for (int x : v) { cur = std::max(0, cur + x); best = std::max(best, cur); }
    return best;
}
std::vector<int> arr_flatten_2d(const std::vector<std::vector<int>>& vv) {
    std::vector<int> r;
    for (auto& row : vv) r.insert(r.end(), row.begin(), row.end());
    return r;
}
int arr_dot_product(const std::vector<int>& a, const std::vector<int>& b) {
    int r = 0;
    for (size_t i = 0; i < std::min(a.size(), b.size()); i++) r += a[i] * b[i];
    return r;
}
void arr_scale(std::vector<int>& v, int factor) { for (auto& x : v) x *= factor; }
void arr_clamp_all(std::vector<int>& v, int lo, int hi) { for (auto& x : v) x = math_clamp(x, lo, hi); }
int  arr_count_if(const std::vector<int>& v, int threshold) {
    return (int)std::count_if(v.begin(), v.end(), [threshold](int x) { return x > threshold; });
}

// --- Utility operations ---

int  util_clamp_int(int x, int lo, int hi) { return x < lo ? lo : x > hi ? hi : x; }
void util_swap_int(int& a, int& b) { std::swap(a, b); }
bool util_is_digit(char c) { return isdigit((unsigned char)c); }
bool util_is_alpha(char c) { return isalpha((unsigned char)c); }
bool util_is_space(char c) { return isspace((unsigned char)c); }
char util_to_upper_char(char c) { return (char)toupper((unsigned char)c); }
char util_to_lower_char(char c) { return (char)tolower((unsigned char)c); }
unsigned util_hash_simple(const std::string& s) {
    unsigned h = 5381;
    for (unsigned char c : s) h = h * 33 ^ c;
    return h;
}
int util_count_bits(unsigned x) {
    int n = 0; while (x) { n += x & 1; x >>= 1; } return n;
}
unsigned util_reverse_bits(unsigned x) {
    unsigned r = 0;
    for (int i = 0; i < 32; i++) { r = (r << 1) | (x & 1); x >>= 1; }
    return r;
}
int util_popcount(unsigned x) { return __builtin_popcount(x); }
int util_log2_int(unsigned x) { return x == 0 ? -1 : 31 - __builtin_clz(x); }
unsigned util_next_power2(unsigned x) {
    if (x == 0) return 1;
    x--;
    x |= x >> 1; x |= x >> 2; x |= x >> 4; x |= x >> 8; x |= x >> 16;
    return x + 1;
}
unsigned util_align_up(unsigned x, unsigned align)   { return (x + align - 1) & ~(align - 1); }
unsigned util_align_down(unsigned x, unsigned align) { return x & ~(align - 1); }
bool util_in_range(int x, int lo, int hi) { return x >= lo && x <= hi; }
double util_clamp_double(double x, double lo, double hi) { return x < lo ? lo : x > hi ? hi : x; }
double util_lerp_double(double a, double b, double t) { return a + t * (b - a); }
bool util_parse_bool(const std::string& s) {
    return s == "true" || s == "1" || s == "yes" || s == "on";
}
long util_timestamp_ms() {
    return std::chrono::duration_cast<std::chrono::milliseconds>(
        std::chrono::system_clock::now().time_since_epoch()).count();
}
std::string util_format_size(long bytes) {
    const char* units[] = {"B","KB","MB","GB","TB"};
    int i = 0;
    double v = bytes;
    while (v >= 1024 && i < 4) { v /= 1024; i++; }
    std::ostringstream oss;
    oss << (int)v << units[i];
    return oss.str();
}
void util_sleep_ms(int ms) { std::this_thread::sleep_for(std::chrono::milliseconds(ms)); }
unsigned util_xorshift32(unsigned x) { x ^= x << 13; x ^= x >> 17; x ^= x << 5; return x; }
unsigned util_rotate_left32(unsigned x, int n)  { return (x << n) | (x >> (32 - n)); }
unsigned util_rotate_right32(unsigned x, int n) { return (x >> n) | (x << (32 - n)); }
