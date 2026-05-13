CXX      = g++
CXXFLAGS = -g -O0 -std=c++17
TARGET   = sample
SRCS     = main.cpp functions.cpp

$(TARGET): $(SRCS) functions.h
	$(CXX) $(CXXFLAGS) -o $@ $(SRCS)

clean:
	rm -f $(TARGET)

.PHONY: clean
