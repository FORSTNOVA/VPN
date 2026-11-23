package main

type converterSs struct {
	Method   string `json:"method"`
	Password string `json:"password"`
}

type converterVmess struct {
	UUID     string `json:"uuid"`
	Security string `json:"security"`
	AlterID  int    `json:"alter_id"`
	TLS      *struct {
		Enabled    bool     `json:"enabled"`
		ServerName string   `json:"server_name"`
		Insecure   bool     `json:"insecure"`
		ALPN       []string `json:"alpn"`
	} `json:"tls"`
	Transport *struct {
		Type    string            `json:"type"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	} `json:"transport"`
}

type converterVless struct {
	UUID string `json:"uuid"`
	Flow string `json:"flow"`
	TLS  *struct {
		Enabled    bool     `json:"enabled"`
		ServerName string   `json:"server_name"`
		Insecure   bool     `json:"insecure"`
		ALPN       []string `json:"alpn"`
		Reality    *struct {
			Enabled   bool   `json:"enabled"`
			PublicKey string `json:"public_key"`
			ShortID   string `json:"short_id"`
		} `json:"reality"`
	} `json:"tls"`
	Transport *struct {
		Type    string            `json:"type"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	} `json:"transport"`
}

type converterTrojan struct {
	Password string `json:"password"`
	TLS      *struct {
		Enabled    bool     `json:"enabled"`
		ServerName string   `json:"server_name"`
		Insecure   bool     `json:"insecure"`
		ALPN       []string `json:"alpn"`
	} `json:"tls"`
	Transport *struct {
		Type    string            `json:"type"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	} `json:"transport"`
}

type converterHy2 struct {
	Password string `json:"password"`
	TLS      *struct {
		ServerName string   `json:"server_name"`
		Insecure   bool     `json:"insecure"`
		ALPN       []string `json:"alpn"`
	} `json:"tls"`
	Obfs *struct {
		Type     string `json:"type"`
		Password string `json:"password"`
	} `json:"obfs"`
}

type converterTuic struct {
	UUID                 string `json:"uuid"`
	Password             string `json:"password"`
	CongestionController string `json:"congestion_controller"`
	TLS                  *struct {
		ServerName string   `json:"server_name"`
		Insecure   bool     `json:"insecure"`
		ALPN       []string `json:"alpn"`
	} `json:"tls"`
}
