// Fixture for test_sigchld_reaper.sh (issue #167): a daemon that, like
// vsftpd's standalone listener, reaps every child from a SIGCHLD handler and
// assumes each reaped pid is one it forked. vsftpd NULL-derefs on a stranger;
// this exits 4 instead.
//   reaper SECS   wait SECS for SIGCHLD, then call late_marker()
#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/wait.h>
#include <unistd.h>

static volatile sig_atomic_t got_chld;
static void on_chld(int sig) { (void)sig; got_chld = 1; }

// Recorded only if the tracer is still attached SECS after exec.
__attribute__((noinline)) void late_marker(void) { puts("LATE"); }

int main(int argc, char **argv) {
    int secs = argc > 1 ? atoi(argv[1]) : 1;

    // A freshly exec'd daemon has forked nothing: any child is a stranger.
    pid_t r = waitpid(-1, NULL, WNOHANG);
    if (r != -1 || errno != ECHILD)
        printf("INHERITED_CHILD %d\n", (int)r);
    fflush(stdout);

    struct sigaction sa = {.sa_handler = on_chld};
    sigaction(SIGCHLD, &sa, NULL);
    for (int i = 0; i < secs * 10; i++) {
        usleep(100000);
        if (!got_chld)
            continue;
        pid_t p;
        while ((p = waitpid(-1, NULL, WNOHANG)) > 0) {
            printf("REAPED_UNKNOWN_CHILD %d\n", (int)p);
            return 4;
        }
        got_chld = 0;
    }
    late_marker();
    return 0;
}
