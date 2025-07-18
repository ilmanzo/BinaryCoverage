#ifndef FUNCTRACER_HPP
#define FUNCTRACER_HPP

#include <string>
#include <array>
#include <algorithm>
#include <string_view>

// Implementation of starts_with and ends_with since we can't use c++20 features
inline bool starts_with(std::string_view str, std::string_view prefix) {
    return str.size() >= prefix.size() && str.substr(0, prefix.size()) == prefix;
}
inline bool ends_with(std::string_view str, std::string_view suffix) {
    return str.size() >= suffix.size() &&
           str.substr(str.size() - suffix.size()) == suffix;
}

bool func_is_relevant(const std::string_view &func_name)
{
    static constexpr std::array<std::string_view, 5> blacklist = {
        "main", "_init", "_start", ".plt.got", ".plt"
    };

    if (std::find(blacklist.begin(), blacklist.end(), func_name) != blacklist.end())
        return false;

    if (ends_with(func_name, "@plt") || starts_with(func_name, "__"))
        return false;

    return true;
}

bool image_is_relevant(const std::string_view &image_name)
{
    static constexpr std::array<std::string_view, 1> blacklist = {
        "[vdso]"
    };

    return std::find(blacklist.begin(), blacklist.end(), image_name) == blacklist.end();
}

#endif // FUNCTRACER_HPP
