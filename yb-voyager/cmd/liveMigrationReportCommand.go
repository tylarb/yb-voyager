/*
Copyright (c) YugabyteDB, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cmd

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/gosuri/uitable"
	"github.com/spf13/cobra"
	"github.com/yugabyte/yb-voyager/yb-voyager/src/datafile"
	"github.com/yugabyte/yb-voyager/yb-voyager/src/dbzm"
	"github.com/yugabyte/yb-voyager/yb-voyager/src/metadb"
	"github.com/yugabyte/yb-voyager/yb-voyager/src/tgtdb"
	"github.com/yugabyte/yb-voyager/yb-voyager/src/utils"
)

var liveMigrationReportCmd = &cobra.Command{
	Use:   "report",
	Short: "This command will print the report of any live migration workflow.",
	Long:  `This command will print the report of the live migration or live migration with fall-forward or live migration with fall-back.`,

	Run: func(cmd *cobra.Command, args []string) {
		migrationStatus, err := metaDB.GetMigrationStatusRecord()
		if err != nil {
			utils.ErrExit("error while getting migration status: %w\n", err)
		}
		streamChanges, err := checkWithStreamingMode()
		if err != nil {
			utils.ErrExit("error while checking streaming mode: %w\n", err)
		}
		migrationUUID, err = uuid.Parse(migrationStatus.MigrationUUID)
		if err != nil {
			utils.ErrExit("error while parsing migration UUID: %w\n", err)
		}
		if streamChanges {
			getTargetPassword(cmd)
			migrationStatus.TargetDBConf.Password = tconf.Password
			if migrationStatus.FallForwardEnabled {
				getFallForwardDBPassword(cmd)
				migrationStatus.FallForwardDBConf.Password = tconf.Password
			}
			if migrationStatus.FallbackEnabled {
				getSourceDBPassword(cmd)
				migrationStatus.SourceDBAsTargetConf.Password = source.Password
			}
			liveMigrationStatusCmdFn(migrationStatus)
		} else {
			utils.ErrExit("live-migration report is only applicable when export-type is 'snapshot-and-changes' in the migration")
		}
	},
}

type rowData struct {
	TableName        string
	DBType           string
	SnapshotRowCount int64
	InsertsIn        int64
	UpdatesIn        int64
	DeletesIn        int64
	InsertsOut       int64
	UpdatesOut       int64
	DeletesOut       int64
}

var fBEnabled, fFEnabled bool

func liveMigrationStatusCmdFn(msr *metadb.MigrationStatusRecord) {
	fBEnabled = msr.FallbackEnabled
	fFEnabled = msr.FallForwardEnabled
	tableList := msr.TableListExportedFromSource
	uitbl := uitable.New()
	uitbl.MaxColWidth = 50
	uitbl.Separator = " | "

	addHeader(uitbl, "TABLE", "DB TYPE", "SNAPSHOT ROW COUNT", "EXPORTED", "EXPORTED", "EXPORTED", "IMPORTED", "IMPORTED", "IMPORTED", "FINAL ROW COUNT")
	addHeader(uitbl, "", "", "", "INSERTS", "UPDATES", "DELETES", "INSERTS", "UPDATES", "DELETES", "")
	exportStatusFilePath := filepath.Join(exportDir, "data", "export_status.json")
	dbzmStatus, err := dbzm.ReadExportStatus(exportStatusFilePath)
	if err != nil {
		utils.ErrExit("Failed to read export status file %s: %v", exportStatusFilePath, err)
	}

	source = *msr.SourceDBConf
	sourceSchemaCount := len(strings.Split(source.Schema, "|"))

	for _, table := range tableList {
		uitbl.AddRow() // blank row

		row := rowData{}
		tableName := strings.Split(table, ".")[1]
		schemaName := strings.Split(table, ".")[0]
		tableExportStatus := dbzmStatus.GetTableExportStatus(tableName, schemaName)
		if tableExportStatus == nil {
			tableExportStatus = &dbzm.TableExportStatus{
				TableName:                tableName,
				SchemaName:               schemaName,
				ExportedRowCountSnapshot: 0,
				FileName:                 "",
			}
		}
		row.SnapshotRowCount = tableExportStatus.ExportedRowCountSnapshot
		row.TableName = table
		if sourceSchemaCount <= 1 {
			schemaName = ""
			row.TableName = tableName
		}
		row.DBType = "Source"
		err := updateExportedEventsCountsInTheRow(&row, tableName, schemaName) //source OUT counts
		if err != nil {
			utils.ErrExit("error while getting exported events counts for source DB: %w\n", err)
		}
		if fBEnabled {
			err = updateImportedEventsCountsInTheRow(&row, tableName, schemaName, msr.SourceDBAsTargetConf) //fall back IN counts
			if err != nil {
				utils.ErrExit("error while getting imported events for source DB in case of fall-back: %w\n", err)
			}
		}
		addRowInTheTable(uitbl, row)

		row = rowData{}
		row.TableName = ""
		row.DBType = "Target"
		err = updateImportedEventsCountsInTheRow(&row, tableName, schemaName, msr.TargetDBConf) //target IN counts
		if err != nil {
			utils.ErrExit("error while getting imported events for target DB: %w\n", err)
		}
		if fFEnabled || fBEnabled {
			err = updateExportedEventsCountsInTheRow(&row, tableName, schemaName) // target OUT counts
			if err != nil {
				utils.ErrExit("error while getting exported events for target DB: %w\n", err)
			}
		}
		addRowInTheTable(uitbl, row)
		if fFEnabled {
			row = rowData{}
			row.TableName = ""
			row.DBType = "Fall Forward"
			err = updateImportedEventsCountsInTheRow(&row, tableName, schemaName, msr.FallForwardDBConf) //fall forward IN counts
			if err != nil {
				utils.ErrExit("error while getting imported events for fall-forward DB: %w\n", err)
			}
			addRowInTheTable(uitbl, row)
		}
	}
	if len(tableList) > 0 {
		fmt.Print("\n")
		fmt.Println(uitbl)
		fmt.Print("\n")
	}

}

func addRowInTheTable(uitbl *uitable.Table, row rowData) {
	uitbl.AddRow(row.TableName, row.DBType, row.SnapshotRowCount, row.InsertsOut, row.UpdatesOut, row.DeletesOut, row.InsertsIn, row.UpdatesIn, row.DeletesIn, getFinalRowCount(row))
}

func updateImportedEventsCountsInTheRow(row *rowData, tableName string, schemaName string, targetConf *tgtdb.TargetConf) error {
	switch row.DBType {
	case "Target":
		importerRole = TARGET_DB_IMPORTER_ROLE
	case "Fall Forward":
		importerRole = FF_DB_IMPORTER_ROLE
	case "Source":
		importerRole = FB_DB_IMPORTER_ROLE
	}
	//reinitialise targetDB
	tconf = *targetConf
	tdb = tgtdb.NewTargetDB(&tconf)
	err := tdb.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize the target DB: %w", err)
	}
	defer tdb.Finalize()
	err = tdb.InitConnPool()
	if err != nil {
		return fmt.Errorf("failed to initialize the target DB connection pool: %w", err)
	}
	var dataFileDescriptor *datafile.Descriptor

	state := NewImportDataState(exportDir)
	dataFileDescriptorPath := filepath.Join(exportDir, datafile.DESCRIPTOR_PATH)
	if utils.FileOrFolderExists(dataFileDescriptorPath) {
		dataFileDescriptor = datafile.OpenDescriptor(exportDir)
	} else {
		return fmt.Errorf("data file descriptor not found at %s for snapshot", dataFileDescriptorPath)
	}

	dataFile := dataFileDescriptor.GetDataFileEntryByTableName(tableName)
	if dataFile == nil {
		dataFile = &datafile.FileEntry{
			FilePath:  "",
			TableName: tableName,
			FileSize:  0,
			RowCount:  0,
		}
	}

	if importerRole != FB_DB_IMPORTER_ROLE {
		row.SnapshotRowCount, err = state.GetImportedRowCount(dataFile.FilePath, dataFile.TableName)
		if err != nil {
			return fmt.Errorf("get imported row count for table %q for DB type %s: %w", tableName, row.DBType, err)
		}
	}

	eventCounter, err := state.GetImportedEventsStatsForTable(tableName, migrationUUID)
	if err != nil {
		return fmt.Errorf("get imported events stats for table %q for DB type %s: %w", tableName, row.DBType, err)
	}
	row.InsertsIn = eventCounter.NumInserts
	row.UpdatesIn = eventCounter.NumUpdates
	row.DeletesIn = eventCounter.NumDeletes
	return nil
}

func updateExportedEventsCountsInTheRow(row *rowData, tableName string, schemaName string) error {
	switch row.DBType {
	case "Source":
		exporterRole = SOURCE_DB_EXPORTER_ROLE
	case "Target":
		if fFEnabled {
			exporterRole = TARGET_DB_EXPORTER_FF_ROLE
		} else if fBEnabled {
			exporterRole = TARGET_DB_EXPORTER_FB_ROLE
		}
	}
	eventCounter, err := metaDB.GetExportedEventsStatsForTableAndExporterRole(exporterRole, schemaName, tableName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("could not fetch table stats from meta DB: %w", err)
	}
	row.InsertsOut = eventCounter.NumInserts
	row.UpdatesOut = eventCounter.NumUpdates
	row.DeletesOut = eventCounter.NumDeletes
	return nil
}

func getFinalRowCount(row rowData) int64 {
	return row.SnapshotRowCount + row.InsertsIn + row.InsertsOut - row.DeletesIn - row.DeletesOut
}

func init() {
	liveMigrationCommand.AddCommand(liveMigrationReportCmd)
	registerCommonGlobalFlags(liveMigrationReportCmd)
	liveMigrationReportCmd.Flags().StringVar(&ffDbPassword, "ff-db-password", "",
		"password with which to connect to the target fall-forward DB server. Alternatively, you can also specify the password by setting the environment variable FF_DB_PASSWORD. If you don't provide a password via the CLI, yb-voyager will prompt you at runtime for a password. If the password contains special characters that are interpreted by the shell (for example, # and $), enclose the password in single quotes.")

	liveMigrationReportCmd.Flags().StringVar(&sourceDbPassword, "source-db-password", "",
		"password with which to connect to the target source DB server. Alternatively, you can also specify the password by setting the environment variable SOURCE_DB_PASSWORD. If you don't provide a password via the CLI, yb-voyager will prompt you at runtime for a password. If the password contains special characters that are interpreted by the shell (for example, # and $), enclose the password in single quotes")

	liveMigrationReportCmd.Flags().StringVar(&targetDbPassword, "target-db-password", "",
		"password with which to connect to the target YugabyteDB server. Alternatively, you can also specify the password by setting the environment variable TARGET_DB_PASSWORD. If you don't provide a password via the CLI, yb-voyager will prompt you at runtime for a password. If the password contains special characters that are interpreted by the shell (for example, # and $), enclose the password in single quotes.")
}