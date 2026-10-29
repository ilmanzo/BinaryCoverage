// Fixture for test_bystander_traps.sh (issue #163).
//   hold SECS    stay alive (keeps a traced instance's probes attached)
//   spin N       call hot() in libhot.so N times
//   fork MS      child calls child_early() at once, child_late() after MS ms
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

void hot(unsigned long i);

__attribute__((noinline)) void child_early(void) { hot(0); }
__attribute__((noinline)) void child_late(void) { hot(1); }

int main(int argc, char **argv) {
    if (argc != 3) {
        fprintf(stderr, "usage: %s hold SECS | spin N | fork MS\n", argv[0]);
        return 2;
    }
    long n = atol(argv[2]);
    if (!strcmp(argv[1], "hold")) {
        sleep(n);
    } else if (!strcmp(argv[1], "spin")) {
        for (long i = 0; i < n; i++)
            hot(i);
    } else if (!strcmp(argv[1], "fork")) {
        pid_t pid = fork();
        if (pid == 0) {
            child_early();
            usleep(n * 1000);
            child_late();
            _exit(0);
        }
        waitpid(pid, NULL, 0);
    } else {
        return 2;
    }
    return 0;
}
