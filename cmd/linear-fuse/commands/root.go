package commands

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	apiKey  string
)

var rootCmd = &cobra.Command{
	Use:   "linear-fuse",
	Short: "Mount Linear.app issues as a filesystem",
	Long: `Linear FUSE is a filesystem that allows you to interact with Linear.app 
issues as text files with YAML frontmatter for metadata.`,
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.linear-fuse.yaml)")
	rootCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "Linear API key")

	viper.BindPFlag("api-key", rootCmd.PersistentFlags().Lookup("api-key"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".linear-fuse")
	}

	viper.SetEnvPrefix("LINEAR")
	viper.AutomaticEnv()

	viper.ReadInConfig()
}
