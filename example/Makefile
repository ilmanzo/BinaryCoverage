all: cov_sample dlopen_test

cov_sample:
	${CC} -g -gdwarf-4 main.c calc.c -o cov_sample

dlopen_test: libplugin.so
	${CC} -g -gdwarf-4 main_dlopen.c -o dlopen_test -ldl

libplugin.so:
	${CC} -g -gdwarf-4 -shared -fPIC libplugin.c -o libplugin.so

clean:
	rm -f cov_sample dlopen_test libplugin.so

