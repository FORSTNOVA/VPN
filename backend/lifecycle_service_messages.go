package main

import "fmt"

func lifecycleLegacyChainName() string { return "我的代理链" }

func lifecycleLog(code int, name string, err error) string {
	switch code {
	case 0:
		return fmt.Sprintf("could not read the stored subscription URL: %v", err)
	case 1:
		return fmt.Sprintf("could not read the %s that came with the package: %v", name, err)
	case 2:
		return fmt.Sprintf("could not place the %s that came with the package: %v", name, err)
	case 3:
		return fmt.Sprintf("adopted the %s that came with the package", name)
	case 4:
		return fmt.Sprintf("could not open the local database: %v", err)
	case 5:
		return fmt.Sprintf("%v", err)
	case 6:
		return fmt.Sprintf("could not load node health: %v", err)
	case 7:
		return fmt.Sprintf("could not load exit regions: %v", err)
	case 8:
		return "another SmartVPN is serving the Windows proxy; leaving it as it is"
	case 9:
		return fmt.Sprintf("could not recover previous system proxy settings: %v", err)
	}
	return ""
}
