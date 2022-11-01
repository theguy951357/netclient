/*
Copyright © 2022 Netmaker Team <info@netmaker.io>
*/
package cmd

import (
	"fmt"
	"os"

	"github.com/gravitl/netclient/config"
	"github.com/gravitl/netmaker/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "netclient",
	Short: "Netmaker's netclient agent and CLI",
	Long: `Netmaker's netclient agent and CLI to manage wireguard networks

Join, leave, connect and disconnect from netmaker wireguard networks.`,
	// Uncomment the following line if your bare application
	// has an action associated with it:
	//Run: func(cmd *cobra.Command, args []string) {},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "use specified config file")
	rootCmd.PersistentFlags().IntP("verbosity", "v", 0, "set loggin verbosity 0-4")
	viper.BindPFlags(rootCmd.Flags())

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		viper.AddConfigPath(config.GetNetclientPath())
		viper.SetConfigName("netclient.yml")
	}
	viper.SetConfigType("yml")

	viper.BindPFlags(rootCmd.PersistentFlags())
	viper.AutomaticEnv() // read in environment variables that match
	//not sure why vebosity not set in AutomaticEnv
	viper.BindEnv("verbosity", "VERBOSITY")

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		logger.Log(0, "Using config file:", viper.ConfigFileUsed())
	} else {
		logger.Log(0, "error reading config file", err.Error())
	}

	var Netclient config.Config

	if err := viper.Unmarshal(&Netclient); err != nil {
		logger.Log(0, "could not read netclient config file", err.Error())
	}
	logger.Verbosity = Netclient.Verbosity
	config.Netclient = Netclient
	fmt.Println("verbosity set to ", logger.Verbosity)
	config.GetNodes()
	config.GetServers()
	//check netclient dirs exist
	logger.Log(0, "checking netclient paths")
	if _, err := os.Stat(config.GetNetclientInterfacePath()); err != nil {
		if os.IsNotExist(err) {
			if err := os.Mkdir(config.GetNetclientInterfacePath(), os.ModePerm); err != nil {
				logger.Log(0, "failed to create dirs", err.Error())
			}
		} else {
			logger.FatalLog("could not create /etc/netclient dir" + err.Error())
		}
	}
}