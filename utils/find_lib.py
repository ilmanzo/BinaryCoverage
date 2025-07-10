#!/usr/bin/env python3

#
# find_lib.py
# This script finds the shared library that exports a given function for a specified binary.
# It uses `ldd` to list the libraries linked by the binary and `nm` to check for the function in those libraries.
# It is designed to be run from the command line with two arguments: the binary path and the function name.



import subprocess
import sys
import re
import os

def find_function_in_libraries(binary_path, function_name):
    """
    Finds the shared library that exports a given function for a specified binary.

    Args:
        binary_path (str): The full path to the executable binary.
        function_name (str): The name of the function to search for.
    """
    print(f"Searching for function '{function_name}' in libraries linked by '{binary_path}'...")

    # --- 1. Validate binary path ---
    if not os.path.exists(binary_path):
        print(f"Error: Binary '{binary_path}' not found.")
        return
    if not os.path.isfile(binary_path):
        print(f"Error: '{binary_path}' is not a file.")
        return
    if not os.access(binary_path, os.X_OK):
        print(f"Warning: '{binary_path}' is not executable. ldd might not work as expected.")

    # --- 2. Get shared libraries using ldd ---
    try:
        # Execute ldd command to list dynamic dependencies
        # text=True decodes stdout/stderr as text using default encoding
        # stderr=subprocess.PIPE captures stderr to prevent it from printing to console
        ldd_process = subprocess.run(['ldd', binary_path], capture_output=True, text=True, check=True)
        ldd_output = ldd_process.stdout
    except FileNotFoundError:
        print("Error: 'ldd' command not found. Please ensure it's in your system's PATH.")
        return
    except subprocess.CalledProcessError as e:
        print(f"Error running ldd on '{binary_path}':")
        print(f"  Command: {' '.join(e.cmd)}")
        print(f"  Return Code: {e.returncode}")
        print(f"  Stderr: {e.stderr.strip()}")
        return
    except Exception as e:
        print(f"An unexpected error occurred while running ldd: {e}")
        return

    # --- 3. Parse ldd output to extract unique, existing library paths ---
    library_paths = set()
    for line in ldd_output.splitlines():
        # ldd output lines can be like:
        #   libfoo.so.X => /path/to/libfoo.so.X (0x...)
        #   /lib64/ld-linux-x86-64.so.2 (0x...)  (for the dynamic linker itself)
        # We want the full path to the .so file.

        lib_path = None
        # Try to extract path from "=> /path/to/lib (0x...)" format
        match = re.search(r'=>\s*(\S+)\s*\(0x[0-9a-fA-F]+\)', line)
        if match:
            lib_path = match.group(1)
        else:
            # Try to extract path from "/path/to/lib (0x...)" format (e.g., for ld-linux.so)
            parts = line.strip().split()
            if len(parts) >= 2 and parts[0].startswith('/') and '.so' in parts[0] and parts[1].startswith('(0x'):
                lib_path = parts[0]

        # Add to set if it's a valid, existing file and not the virtual vdso
        if lib_path and os.path.exists(lib_path) and not "linux-vdso.so" in lib_path:
            # Use realpath to resolve any symlinks to the actual file
            library_paths.add(os.path.realpath(lib_path))

    if not library_paths:
        print(f"No shared libraries found for '{binary_path}'. It might be statically linked or missing dependencies.")
        return

    print(f"Found {len(library_paths)} unique shared libraries to check.")

    # --- 4. Iterate through libraries and use nm to find the function ---
    found_in_library = None
    # Sort for consistent output order, though not strictly necessary for functionality
    for lib_path in sorted(list(library_paths)):
        print(f"  Checking '{lib_path}'...")
        try:
            # Execute nm -D (or --dynamic) to list dynamic symbols (exported/imported)
            # We are looking for 'T' (Text/Code) or 'W' (Weak) symbols, which are definitions.
            nm_process = subprocess.run(['nm', '-D', lib_path], capture_output=True, text=True, check=True)
            nm_output = nm_process.stdout

            # Search for the function name as a whole word, and specifically look for 'T' or 'W' type symbols.
            # Example nm output line: "0000000000080ed0 T fopen"
            # Regex: ^[0-9a-fA-F]+  -> Start of line, followed by address
            #        \s+          -> one or more whitespace characters
            #        ([TW])       -> Capturing group for 'T' (Text/Code) or 'W' (Weak) symbol type
            #        \s+          -> one or more whitespace characters
            #        {function_name}$ -> The exact function name followed by end of line
            function_pattern = r'^[0-9a-fA-F]+\s+([TW])\s+' + re.escape(function_name) + r'$'

            if re.search(function_pattern, nm_output, re.MULTILINE):
                found_in_library = lib_path
                break # Function found, no need to check further libraries

        except FileNotFoundError:
            print(f"  Warning: 'nm' command not found. Skipping '{lib_path}'.")
            continue
        except subprocess.CalledProcessError as e:
            # nm might return a non-zero exit code if no dynamic symbols are found,
            # or if the file is not a valid object file. We can ignore these for our purpose.
            # print(f"  Warning: nm failed on '{lib_path}': {e.stderr.strip()}")
            continue
        except Exception as e:
            print(f"  An unexpected error occurred while processing '{lib_path}': {e}")
            continue

    # --- 5. Report results ---
    if found_in_library:
        print(f"\nSuccess: Function '{function_name}' is defined in: {found_in_library}")
    else:
        print(f"\nResult: Function '{function_name}' not found in any of the directly linked shared libraries.")
        print("Note: This script only checks directly linked libraries and their global/weak symbols.")
        print("It does not account for functions loaded dynamically at runtime (e.g., via dlopen/dlsym),")
        print("or functions that are statically linked into the binary itself.")

# --- Main execution block ---
if __name__ == "__main__":
    # Check for correct number of command-line arguments
    if len(sys.argv) != 3:
        print("Usage: python find_lib.py <binary_path> <function_name>")
        print("Example: python find_lib.py /usr/bin/giftogd2 gdImageGd2")
        sys.exit(1)

    binary_path = sys.argv[1]
    function_name = sys.argv[2]

    find_function_in_libraries(binary_path, function_name)