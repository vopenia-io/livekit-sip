#ifndef GPUPROBE_H
#define GPUPROBE_H

#include <stdint.h>

typedef struct {
    uint32_t sm_util;   // 0-100 GPU SM (compute) utilization
    uint32_t enc_util;  // 0-100 encoder utilization
    uint32_t dec_util;  // 0-100 decoder utilization
} GPUSample;

// gpuprobe_init opens libnvidia-ml.so.1 via dlopen, calls nvmlInit,
// and acquires device handle 0. Returns 0 on success, -1 if NVML
// is unavailable (no GPU, no driver, dlopen fails).
int gpuprobe_init(void);

// gpuprobe_sample fills *out with current GPU utilization rates.
// Returns 0 on success, -1 on error.
int gpuprobe_sample(GPUSample *out);

// gpuprobe_shutdown calls nvmlShutdown and dlcloses the library.
void gpuprobe_shutdown(void);

#endif
