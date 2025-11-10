/* FuncTracer.cpp */
#include "pin.H"
#include <iostream>
#include <fstream>
#include <sstream>
#include <unistd.h> // For getpid()
#include "FuncTracer.hpp"

using namespace std;

// Global set and mutex to track logged functions
static set<string> logged_functions;
static mutex log_mutex;

void log_function_call(const char* img_name, const char* func_name)
{
    string log_key;
    {
        lock_guard<mutex> guard(log_mutex);
        log_key = string(img_name) + ":" + func_name;
        if (logged_functions.contains(log_key))
            return;
        logged_functions.insert(log_key);
    }

    pid_t pid;
    PIN_LockClient();
    pid = PIN_GetPid();
    PIN_UnlockClient();

    ostringstream oss;
    oss << "[PID:" << pid << "] [Image:" << img_name << "] [Called:" << func_name << "]\n";
    LOG(oss.str());
}

// Pin calls this function for every image loaded into the process's address space.
// An image is either an executable or a shared library.
VOID image_load(IMG img, VOID *v)
{
    const string &image_name = IMG_Name(img);
    if (!image_is_relevant(image_name)) // Check if the image is relevant for our analysis
    {
        LOG("[Image:" + image_name + "] is not relevant, skipping...\n");
        return; // Skip irrelevant images
    }
    // We iterate through all the sections of the image.
    for (SEC sec = IMG_SecHead(img); SEC_Valid(sec); sec = SEC_Next(sec))
    {
        LOG("[Image:" + image_name + "] [Section:" + SEC_Name(sec) + "]\n");
        // We iterate through all the routines (functions) in the image.
        if (SEC_Type(sec) != SEC_TYPE_EXEC)
            continue; // Only instrument executable sections
        for (RTN rtn = SEC_RtnHead(sec); RTN_Valid(rtn); rtn = RTN_Next(rtn))
        {
            RTN_Open(rtn);
            const string &rtn_name = RTN_Name(rtn);
            if (func_is_relevant(rtn_name)) // Check if the function is relevant for our analysis
            {
                ostringstream oss;
                // We log the image name and function name so we can see which function is being instrumented.
                oss << "[Image:" << image_name << "] [Function:" << RTN_Name(rtn) << "]\n";
                LOG(oss.str());
                // For each routine, we insert a call to our analysis function `log_function_call`.
                RTN_InsertCall(rtn, IPOINT_BEFORE, (AFUNPTR)log_function_call,
                               IARG_PTR, image_name.c_str(),
                               IARG_PTR, rtn_name.c_str(),
                               IARG_END);
            }
            RTN_Close(rtn);
        }
    }
}

// Pin calls this function when the application is about to fork a new process.
// Returning TRUE tells Pin to follow and instrument the child process.
BOOL follow_child_process(CHILD_PROCESS childProcess, VOID *v)
{
    return TRUE; // Follow the child
}

// Pintool (shared library) entry point
int main(int argc, char *argv[])
{
    // Initialize PIN symbols. This is required for routine-level instrumentation.
    PIN_InitSymbols();

    // Initialize PIN. This must be the first function called.
    if (PIN_Init(argc, argv))
    {
        cerr << "PIN_Init failed" << endl;
        return 1;
    }
    // Register the function to be called for every loaded image.
    IMG_AddInstrumentFunction(image_load, 0);

    // install callback to follow the childs
    PIN_AddFollowChildProcessFunction(follow_child_process, 0);

    // Start the program, never returns
    PIN_StartProgram();
    assert(false); // We should never reach here
    return 0;
}
