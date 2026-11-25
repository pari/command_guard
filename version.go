package main

const AppVersion = "0.8.0"

func stampConfigVersion(config map[string]interface{}) {
	config["version"] = AppVersion
}
