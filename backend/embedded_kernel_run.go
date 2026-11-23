package main

// Resource lifetime follows the original adapter; this does not invent rollback.
func runEmbeddedKernelOperation(q embeddedKernelRequest, fx embeddedKernelEffects) embeddedKernelResult {
	defer fx.Release()
	return runSelectedEmbeddedKernel(q, fx)
}
