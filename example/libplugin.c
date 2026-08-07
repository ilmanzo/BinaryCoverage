#include <stdio.h>

int plugin_func(int x) {
    printf("Hello from plugin_func! Input: %d\n", x);
    return x * 2;
}
