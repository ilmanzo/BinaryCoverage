// Shared library both the traced and the untraced process map, like
// libsystemd-shared for systemctl and PID 1 in issue #163.
volatile unsigned long sink;

__attribute__((noinline)) void hot(unsigned long i) { sink += i; }
