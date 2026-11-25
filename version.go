package main

const AppVersion = "0.7.0"

func stampConfigVersion(config map[string]interface{}) {
	config["version"] = AppVersion
}
