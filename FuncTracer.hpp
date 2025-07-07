#ifndef FUNCTRACER_HPP
#define FUNCTRACER_HPP

#include <string>
#include <set>
#include <mutex>

// Determine if function name is relevant to us and if it will be logged
bool func_is_relevant(const std::string_view &func_name)
{
    static const std::set<std::string_view> blacklist = {
        "main", "_init", "_start", ".plt.got"
    };
    if (blacklist.contains(func_name))
        return false;

    // Ignore PLT functions and internal functions (usually prefixed with __)
    if (func_name.ends_with("@plt") || func_name.starts_with("__"))
        return false;

    return true;
}

bool image_is_relevant(const std::string_view &image_name)
{
    static const std::set<std::string_view> blacklist = {
        "[vdso]"
    };
    return (!blacklist.contains(image_name))
}

#endif // FUNCTRACER_HPP