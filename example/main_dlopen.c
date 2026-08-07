#include <stdio.h>
#include <stdlib.h>
#include <dlfcn.h>
#include <unistd.h>

int main() {
    printf("Loading ./libplugin.so...\n");
    void *handle = dlopen("./libplugin.so", RTLD_NOW);
    if (!handle) {
        fprintf(stderr, "dlopen failed: %s\n", dlerror());
        return 1;
    }

    int (*plugin_func)(int) = dlsym(handle, "plugin_func");
    if (!plugin_func) {
        fprintf(stderr, "dlsym failed: %s\n", dlerror());
        dlclose(handle);
        return 1;
    }

    sleep(1); // Give Go shim time to attach uprobes on-the-fly

    int res = plugin_func(21);
    printf("plugin_func returned: %d\n", res);

    sleep(1); // Keep process alive for closing and log draining
    dlclose(handle);
    return 0;
}
