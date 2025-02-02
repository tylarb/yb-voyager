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
	"strings"

	"github.com/spf13/cobra"
	"github.com/yugabyte/yb-voyager/yb-voyager/src/metadb"
	"github.com/yugabyte/yb-voyager/yb-voyager/src/utils"
)

var importDataToSourceReplicaCmd = &cobra.Command{
	Use: "source-replica",
	Short: "Import data into source-replica database to prepare for fall-forward.\n" +
		"For more details and examples, visit https://docs.yugabyte.com/preview/yugabyte-voyager/migrate/live-fall-forward/",
	Long: ``,

	Run: func(cmd *cobra.Command, args []string) {
		importType = SNAPSHOT_AND_CHANGES
		setTargetConfSpecifics(cmd)
		importerRole = SOURCE_REPLICA_DB_IMPORTER_ROLE
		validateFFDBSchemaFlag()
		importDataCmd.PreRun(cmd, args)
		importDataCmd.Run(cmd, args)
	},
}

func setTargetConfSpecifics(cmd *cobra.Command) {
	msr, err := metaDB.GetMigrationStatusRecord()
	if err != nil {
		utils.ErrExit("get migration status record: %v", err)
	}
	sconf := msr.SourceDBConf
	tconf.TargetDBType = sconf.DBType
	if tconf.TargetDBType == POSTGRESQL {
		if cmd.Flags().Lookup("source-replica-db-schema").Changed {
			utils.ErrExit("cannot specify --source-replica-db-schema for PostgreSQL source")
		} else {
			tconf.Schema = strings.Join(strings.Split(sconf.Schema, "|"), ",")
		}
	}
}

func init() {
	importDataToCmd.AddCommand(importDataToSourceReplicaCmd)
	registerCommonGlobalFlags(importDataToSourceReplicaCmd)
	registerCommonImportFlags(importDataToSourceReplicaCmd)
	registerSourceReplicaDBAsTargetConnFlags(importDataToSourceReplicaCmd)
	registerFlagsForSourceReplica(importDataToSourceReplicaCmd)
	registerStartCleanFlag(importDataToSourceReplicaCmd)
	registerImportDataCommonFlags(importDataToSourceReplicaCmd)
	hideImportFlagsInFallForwardOrBackCmds(importDataToSourceReplicaCmd)
}

func registerStartCleanFlag(cmd *cobra.Command) {
	BoolVar(cmd.Flags(), &startClean, "start-clean", false,
		`Starts a fresh import with exported data files present in the export-dir/data directory. 
If any table on source-replica database is non-empty, it prompts whether you want to continue the import without truncating those tables; 
If you go ahead without truncating, then yb-voyager starts ingesting the data present in the data files without upsert mode.
Note that for the cases where a table doesn't have a primary key, this may lead to insertion of duplicate data. To avoid this, exclude the table using the --exclude-file-list or truncate those tables manually before using the start-clean flag (default false)`)
}

func updateFallForwardEnabledInMetaDB() {
	err := metaDB.UpdateMigrationStatusRecord(func(record *metadb.MigrationStatusRecord) {
		record.FallForwardEnabled = true
	})
	if err != nil {
		utils.ErrExit("error while updating fall forward db exists in meta db: %v", err)
	}
}
