/*
 * drcov.cpp
 *
 * A DynamoRIO client for collecting basic block and function coverage.
 *
 * Responsibilities:
 * 1. Initialize DynamoRIO and its symbol manager.
 * 2. Enumerate all functions in the main executable at startup.
 * 3. Register a callback to be invoked for every new basic block.
 * 4. In the callback, insert instrumentation to log the block's execution.
 * 5. In a shutdown event, calculate the final coverage and write the log files.
 */

#include "dr_api.h"
#include "drmgr.h"
#include "drsyms.h"
#include <iostream>
#include <fstream>
#include <string>
#include <unordered_map>
#include <set>
#include <vector>

// --- Global Data Structures ---

// A map to store basic block coverage: { address -> hit_count }
static std::unordered_map<app_pc, uint> block_coverage;
// A set to store the names of all functions found in the main executable.
static std::set<std::string> all_functions;
// A set to store the names of functions that have been executed.
static std::set<std::string> called_functions;

// A mutex to protect our global data structures from race conditions in multi-threaded apps.
static void *mutex;

// --- Helper Functions ---

// This is the "clean call" that gets executed every time an instrumented basic block runs.
static void log_block_hit(app_pc block_start) {
    dr_mutex_lock(mutex);

    // Record the basic block hit.
    block_coverage[block_start]++;

    // Correctly look up the symbol for the given address.
    // We must first find the module containing the address.
    // FIX: Use the correct function name `dr_lookup_module`.
    module_data_t *mod = dr_lookup_module(block_start);
    if (mod != NULL) {
        drsym_info_t sym_info;
        sym_info.struct_size = sizeof(sym_info);
        // Calculate the offset within the module
        size_t mod_offs = block_start - mod->start;
        // Now call the lookup function with the correct arguments
        if (drsym_lookup_address(mod->full_path, mod_offs, &sym_info, DRSYM_DEFAULT_FLAGS) == DRSYM_SUCCESS) {
            called_functions.insert(sym_info.name);
        }
        dr_free_module_data(mod);
    }

    dr_mutex_unlock(mutex);
}

// Writes the final reports to their respective log files.
static void write_final_reports() {
    // --- Write Basic Block Coverage ---
    std::ofstream cov_file("coverage.log");
    cov_file << "--- Basic Block Coverage ---" << std::endl;
    for (const auto &pair : block_coverage) {
        cov_file << "BLOCK: " << pair.first << ", HITS: " << pair.second << std::endl;
    }
    cov_file << "--- End of Basic Block Coverage ---" << std::endl;

    // --- Write Called Functions ---
    std::ofstream called_file("called_functions.log");
    called_file << "--- Called Functions ---" << std::endl;
    for (const auto &func_name : called_functions) {
        called_file << func_name << std::endl;
    }
    called_file << "--- End of Called Functions ---" << std::endl;

    // --- Calculate and Write Not-Called Functions ---
    std::ofstream not_called_file("not_called_functions.log");
    not_called_file << "--- Not Called Functions ---" << std::endl;
    for (const auto &func_name : all_functions) {
        if (called_functions.find(func_name) == called_functions.end()) {
            not_called_file << func_name << std::endl;
        }
    }
    not_called_file << "--- End of Not Called Functions ---" << std::endl;
}

// --- DynamoRIO Event Callbacks ---

// This event is triggered for every new basic block encountered by DynamoRIO.
// This signature matches the `drmgr_insertion_cb_t` type.
static dr_emit_flags_t event_bb_insert(void *drcontext, void *tag, instrlist_t *bb,
                                       instr_t *instr, bool for_trace, bool translating,
                                       void *user_data) {
    // We only want to instrument the start of the basic block.
    if (instr != instrlist_first(bb))
        return DR_EMIT_DEFAULT;

    // Get the starting address of the block.
    app_pc start_pc = instr_get_app_pc(instr);

    // Insert a "clean call" to our logging function.
    dr_insert_clean_call(drcontext, bb, instr, (void *)log_block_hit, false, 1, OPND_CREATE_INTPTR(start_pc));

    return DR_EMIT_DEFAULT;
}

// This event is triggered when the application is about to exit.
static void event_exit(void) {
    write_final_reports();

    // Clean up the symbol manager and the mutex.
    drsym_exit();
    drmgr_exit();
    dr_mutex_destroy(mutex);
}

// This callback is used to iterate over symbols and populate our `all_functions` set.
static bool symbol_enum_cb(const char *name, size_t modoffs, void *data) {
    // We only care about function symbols.
    // A simple heuristic is to check if the name doesn't start with '$' or '_'.
    if (name[0] != '$' && name[0] != '_') {
        all_functions.insert(name);
    }
    return true; // Continue enumeration.
}

// --- Main Entry Point for the Client ---

DR_EXPORT void dr_client_main(client_id_t id, int argc, const char *argv[]) {
    dr_set_client_name("DynamoRIO Code Coverage Client", "http://example.com/");

    // Initialize the required DynamoRIO extensions.
    drmgr_init();
    if (drsym_init(0) != DRSYM_SUCCESS) {
        dr_log(NULL, DR_LOG_ALL, 1, "Warning: unable to initialize symbols");
    }

    // Create a mutex for thread-safe data access.
    mutex = dr_mutex_create();

    // Enumerate all symbols in the main executable to find all functions.
    module_data_t *main_module = dr_get_main_module();
    if (main_module != NULL) {
        drsym_enumerate_symbols(main_module->full_path, symbol_enum_cb, NULL, DRSYM_DEFAULT_FLAGS);
        dr_free_module_data(main_module);
    }

    // Register our event callbacks.
    dr_register_exit_event(event_exit);
    // FIX: Use the correct `drmgr_register_bb_instrumentation_event` function.
    // We provide NULL for the analysis callback and our function for the insertion callback.
    drmgr_register_bb_instrumentation_event(NULL, event_bb_insert, NULL);
}
