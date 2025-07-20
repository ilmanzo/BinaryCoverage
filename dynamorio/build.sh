#Download DynamoRIO
#First, you need to download the DynamoRIO binaries for your system.
#Go to the DynamoRIO GitHub Releases page.
#Find the latest release (e.g., DynamoRIO-Linux-11.90.tar.gz).
#Download the archive for your operating system (e.g., DynamoRIO-Linux-X.X.X-x86_64.tar.gz).
#(take care it's over 400MB) 
#Extract the archive to a known location, for example, ~/dynamorio.
# ln -s $HOME/DynamoRIO-Linux-11.90.20287 $HOME/dynamorio

rm makefile cmake_install.cmake
# Create a build directory
rm -rf build && mkdir build && pushd build

# Run CMake. You MUST provide the path to the `cmake` directory
# inside your extracted DynamoRIO folder.
# Replace '~/dynamorio' with the actual path.

export DYNAMORIO_ROOT=~/dynamorio/

cmake .. -DDynamoRIO_DIR=$DYNAMORIO_ROOT/cmake -DCMAKE_C_COMPILER=gcc-14 -DCMAKE_CXX_COMPILER=g++-14 -DCMAKE_LINKER=/usr/bin/ld

# Compile the project
make

popd

# run the example
$DYNAMORIO_ROOT/bin64/drrun -c build/libdrcov.so -- build/test_program