package main

import (
	"os"

	_ "github.com/go-sql-driver/mysql"
	"github.com/kubecost/disk-autoscaler/pkg/test"
	"github.com/kubecost/disk-autoscaler/pkg/test/mysql"
	"github.com/rs/zerolog/log"
)

func main() {
	args := os.Args[1:]
	if len(args) != 5 {
		log.Error().Msg("need 5 command line args to proceed")
		return
	}
	var dbConnection test.Datainserter
	if args[0] == "mysql" {
		dbConnection = &mysql.MysqlConnection{
			DatabaseIp:   args[1],
			Username:     args[2],
			Password:     args[3],
			DatabaseName: args[4],
		}
	}
	err := dbConnection.InsertPopulationData()
	if err != nil {
		log.Fatal().Msgf("failed to insert data to database %s with err: %v", args[0], err)
		return
	}
	log.Info().Msg("successfully inserted data, proceed to Disk Auto scaler testing")
}
