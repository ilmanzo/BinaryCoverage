/* test_program.c */

#include <stdio.h>
#include <string.h>

void function_a() {
    printf("Inside function_a\n");
}

void function_b() {
    printf("Inside function_b\n");
}

int main(int argc, char *argv[]) {
    printf("Test program started.\n");

    if (argc < 2) {
        printf("Usage: %s <'a' or 'b'>\n", argv[0]);
        function_a();
    } else {
        if (strcmp(argv[1], "a") == 0) {
            function_a();
        } else if (strcmp(argv[1], "b") == 0) {
            function_b();
        } else {
            printf("Unknown argument: %s\n", argv[1]);
        }
    }

    printf("Test program finished.\n");
    return 0;
}
