// noinline, N generated, output depends on every call
#include <stdio.h>
#include <stdlib.h>
#include "funcs.h"

int main(int argc, char **argv)
{
    long loops = argc > 1 ? atol(argv[1]) : 1000;
    int acc = 1;
    for (long i = 0; i < loops; i++)
        for (size_t f = 0; f < NFUNCS; f++)
            acc = fns[f](acc);
    printf("%d\n", acc);
    return 0;
}
