// dlopen-based NVML wrapper for GPU utilization sampling.
// Loads libnvidia-ml.so.1 at runtime — no build-time dependency on
// NVIDIA headers or libraries. Gracefully returns -1 when NVML is
// unavailable.

#include "gpuprobe.h"
#include <dlfcn.h>
#include <stddef.h>
#include <stdio.h>
#include <string.h>

// Minimal NVML type declarations — avoids requiring nvml.h.
typedef int nvmlReturn_t;
#define NVML_SUCCESS 0

typedef void *nvmlDevice_t;

typedef struct {
    unsigned int gpu;    // SM utilization 0-100%
    unsigned int memory; // memory controller utilization 0-100%
} nvmlUtilization_t;

// Function pointer types.
typedef nvmlReturn_t (*fn_nvmlInit)(void);
typedef nvmlReturn_t (*fn_nvmlShutdown)(void);
typedef nvmlReturn_t (*fn_nvmlDeviceGetHandleByIndex)(unsigned int, nvmlDevice_t *);
typedef nvmlReturn_t (*fn_nvmlDeviceGetUtilizationRates)(nvmlDevice_t, nvmlUtilization_t *);
typedef nvmlReturn_t (*fn_nvmlDeviceGetEncoderUtilization)(nvmlDevice_t, unsigned int *, unsigned int *);
typedef nvmlReturn_t (*fn_nvmlDeviceGetDecoderUtilization)(nvmlDevice_t, unsigned int *, unsigned int *);

static struct {
    void          *lib;
    nvmlDevice_t   device;
    fn_nvmlInit                          init;
    fn_nvmlShutdown                      shutdown;
    fn_nvmlDeviceGetHandleByIndex        getHandle;
    fn_nvmlDeviceGetUtilizationRates     getUtil;
    fn_nvmlDeviceGetEncoderUtilization   getEnc;
    fn_nvmlDeviceGetDecoderUtilization   getDec;
} nvml;

int
gpuprobe_init(void)
{
    memset(&nvml, 0, sizeof(nvml));

    nvml.lib = dlopen("libnvidia-ml.so.1", RTLD_LAZY);
    if (!nvml.lib) {
        fprintf(stderr, "gpuprobe: dlopen failed: %s\n", dlerror());
        return -1;
    }

    nvml.init      = (fn_nvmlInit)dlsym(nvml.lib, "nvmlInit_v2");
    nvml.shutdown  = (fn_nvmlShutdown)dlsym(nvml.lib, "nvmlShutdown");
    nvml.getHandle = (fn_nvmlDeviceGetHandleByIndex)dlsym(nvml.lib, "nvmlDeviceGetHandleByIndex_v2");
    nvml.getUtil   = (fn_nvmlDeviceGetUtilizationRates)dlsym(nvml.lib, "nvmlDeviceGetUtilizationRates");
    nvml.getEnc    = (fn_nvmlDeviceGetEncoderUtilization)dlsym(nvml.lib, "nvmlDeviceGetEncoderUtilization");
    nvml.getDec    = (fn_nvmlDeviceGetDecoderUtilization)dlsym(nvml.lib, "nvmlDeviceGetDecoderUtilization");

    if (!nvml.init || !nvml.shutdown || !nvml.getHandle ||
        !nvml.getUtil || !nvml.getEnc || !nvml.getDec) {
        fprintf(stderr, "gpuprobe: dlsym failed for one or more NVML functions\n");
        dlclose(nvml.lib);
        memset(&nvml, 0, sizeof(nvml));
        return -1;
    }

    nvmlReturn_t r = nvml.init();
    if (r != NVML_SUCCESS) {
        fprintf(stderr, "gpuprobe: nvmlInit failed: %d\n", r);
        dlclose(nvml.lib);
        memset(&nvml, 0, sizeof(nvml));
        return -1;
    }

    r = nvml.getHandle(0, &nvml.device);
    if (r != NVML_SUCCESS) {
        fprintf(stderr, "gpuprobe: nvmlDeviceGetHandleByIndex(0) failed: %d\n", r);
        nvml.shutdown();
        dlclose(nvml.lib);
        memset(&nvml, 0, sizeof(nvml));
        return -1;
    }

    return 0;
}

int
gpuprobe_sample(GPUSample *out)
{
    if (!nvml.lib || !out)
        return -1;

    nvmlUtilization_t util;
    if (nvml.getUtil(nvml.device, &util) != NVML_SUCCESS)
        return -1;
    out->sm_util = util.gpu;

    unsigned int enc, encPeriod;
    if (nvml.getEnc(nvml.device, &enc, &encPeriod) != NVML_SUCCESS)
        return -1;
    out->enc_util = enc;

    unsigned int dec, decPeriod;
    if (nvml.getDec(nvml.device, &dec, &decPeriod) != NVML_SUCCESS)
        return -1;
    out->dec_util = dec;

    return 0;
}

void
gpuprobe_shutdown(void)
{
    if (!nvml.lib)
        return;
    if (nvml.shutdown)
        nvml.shutdown();
    dlclose(nvml.lib);
    memset(&nvml, 0, sizeof(nvml));
}
