// simple mysql test data insertion to upload data to a mysql deployment to test Disk auto scaler
package mysql

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"

	_ "github.com/go-sql-driver/mysql"
)

var avialOpenPopulationAPIYears = []string{"2013", "2014"}

type MysqlConnection struct {
	DatabaseIp   string
	Username     string
	Password     string
	DatabaseName string
}

// Struct to represent the population data
type PopulationData struct {
	Data []struct {
		Id         string `json:"ID State"`
		State      string `json:"State"`
		Year       int    `json:"ID Year"`
		Population int    `json:"Population"`
	} `json:"data"`
}

type PopulationCountyData struct {
	Data []struct {
		Id         string `json:"ID County"`
		County     string `json:"County"`
		Year       int    `json:"ID Year"`
		Population int    `json:"Population"`
	} `json:"data"`
}

func (db *MysqlConnection) InsertPopulationData() error {
	dbconnect := fmt.Sprintf("%s:%s@tcp(%s)/%s", db.Username, db.Password, db.DatabaseIp, db.DatabaseName)
	log.Info().Msgf("dbconnect: %s", dbconnect)
	sqlDb, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/%s", db.Username, db.Password, db.DatabaseIp, db.DatabaseName))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %v", err)
	}
	defer sqlDb.Close()

	for _, year := range avialOpenPopulationAPIYears {
		url := fmt.Sprintf("https://datausa.io/api/data?drilldowns=State&measures=Population&year=%s", year)
		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("failed to fetch State data: %v", err)
		}
		defer resp.Body.Close()

		var populationData PopulationData
		if err := json.NewDecoder(resp.Body).Decode(&populationData); err != nil {
			return fmt.Errorf("failed to decode State Data JSON: %v", err)
		}

		stmt, err := sqlDb.Prepare("INSERT INTO population_data (id, state, year, population) VALUES (?, ?, ?, ?)")
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %v", err)
		}
		defer stmt.Close()

		for _, data := range populationData.Data {
			_, err := stmt.Exec(data.Id, data.State, data.Year, data.Population)
			if err != nil {
				return fmt.Errorf("failed to insert data: %v", err)
			}
		}

	}

	log.Info().Msg("State Data inserted successfully!")

	for _, year := range avialOpenPopulationAPIYears {
		countyUrl := fmt.Sprintf("https://datausa.io/api/data?drilldowns=County&measures=Population&year=%s", year)
		respCounty, err := http.Get(countyUrl)
		if err != nil {
			return fmt.Errorf("failed to fetch County data: %v", err)
		}
		defer respCounty.Body.Close()

		var populationCountyData PopulationCountyData
		if err := json.NewDecoder(respCounty.Body).Decode(&populationCountyData); err != nil {
			return fmt.Errorf("failed to decode County Data JSON: %v", err)
		}

		stmtCounty, err := sqlDb.Prepare("INSERT INTO population_county_data (id, county, year, population) VALUES (?, ?, ?, ?)")
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %v", err)
		}
		defer stmtCounty.Close()

		for _, data := range populationCountyData.Data {
			_, err := stmtCounty.Exec(data.Id, data.County, data.Year, data.Population)
			if err != nil {
				return fmt.Errorf("failed to fetch County data: %v", err)
			}
		}
	}
	log.Info().Msg("County Data inserted successfully!")
	return nil
}
