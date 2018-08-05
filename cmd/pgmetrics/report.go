/*
 * Copyright 2018 RapidLoop, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/rapidloop/pgmetrics"
)

func writeHumanTo(fd io.Writer, o options, result *pgmetrics.Model) {
	version := getVersion(result)
	sincePrior, _ := lsnDiff(result.RedoLSN, result.PriorLSN)
	sinceRedo, _ := lsnDiff(result.CheckpointLSN, result.RedoLSN)
	fmt.Fprintf(fd, `
pgmetrics run at: %s

PostgreSQL Cluster:
    Name:                %s
    Server Version:      %s
    Server Started:      %s`,
		fmtTimeAndSince(result.Metadata.At),
		getSetting(result, "cluster_name"),
		getSetting(result, "server_version"),
		fmtTimeAndSince(result.StartTime),
	)
	if version >= 90600 {
		fmt.Fprintf(fd, `
    System Identifier:   %s
    Timeline:            %d
    Last Checkpoint:     %s`,
			result.SystemIdentifier,
			result.TimelineID,
			fmtTimeAndSince(result.CheckpointTime),
		)
		if result.PriorLSN != "" && result.RedoLSN != "" && result.CheckpointLSN != "" {
			fmt.Fprintf(fd, `
    Prior LSN:           %s
    REDO LSN:            %s (%s since Prior)
    Checkpoint LSN:      %s (%s since REDO)`,
				result.PriorLSN,
				result.RedoLSN, humanize.IBytes(uint64(sincePrior)),
				result.CheckpointLSN, humanize.IBytes(uint64(sinceRedo)),
			)
		} else if result.PriorLSN == "" && result.RedoLSN != "" && result.CheckpointLSN != "" {
			fmt.Fprintf(fd, `
    REDO LSN:            %s
    Checkpoint LSN:      %s (%s since REDO)`,
				result.RedoLSN,
				result.CheckpointLSN, humanize.IBytes(uint64(sinceRedo)),
			)
		}
		fmt.Fprintf(fd, `
    Transaction IDs:     %d to %d (diff = %d)`,
			result.OldestXid, result.NextXid-1,
			result.NextXid-1-result.OldestXid,
		)
	}

	if result.LastXactTimestamp != 0 {
		fmt.Fprintf(fd, `
    Last Transaction:    %s`,
			fmtTimeAndSince(result.LastXactTimestamp),
		)
	}

	if version >= 90600 {
		fmt.Fprintf(fd, `
    Notification Queue:  %.1f%% used`, result.NotificationQueueUsage)
	}

	fmt.Fprintf(fd, `
    Active Backends:     %d (max %s)
    Recovery Mode?       %s
`,
		len(result.Backends), getSetting(result, "max_connections"),
		fmtYesNo(result.IsInRecovery),
	)

	if result.System != nil {
		reportSystem(fd, result)
	}

	if result.IsInRecovery {
		reportRecovery(fd, result)
	}

	if result.ReplicationIncoming != nil {
		reportReplicationIn(fd, result)
	}

	if len(result.ReplicationOutgoing) > 0 {
		reportReplicationOut(fd, result)
	}

	if len(result.ReplicationSlots) > 0 {
		reportReplicationSlots(fd, result, version)
	}

	if len(result.Publications) > 0 {
		reportPublications(fd, result)
	}

	if len(result.Subscriptions) > 0 {
		reportSubscriptions(fd, result)
	}

	reportWAL(fd, result)
	reportBGWriter(fd, result)
	reportBackends(fd, o.tooLongSec, result)
	if version >= 90600 {
		reportVacuumProgress(fd, result)
	}
	reportRoles(fd, result)
	reportTablespaces(fd, result)
	reportDatabases(fd, result)
	reportTables(fd, result)
	fmt.Fprintln(fd)
}

func reportRecovery(fd io.Writer, result *pgmetrics.Model) {
	fmt.Fprintf(fd, `
Recovery Status:
    Replay paused:       %s
    Received LSN:        %s
    Replayed LSN:        %s%s
    Last Replayed Txn:   %s
`,
		fmtYesNo(result.IsWalReplayPaused),
		result.LastWALReceiveLSN,
		result.LastWALReplayLSN,
		fmtLag(result.LastWALReceiveLSN, result.LastWALReplayLSN, ""),
		fmtTimeAndSince(result.LastXActReplayTimestamp))
}

func reportReplicationIn(fd io.Writer, result *pgmetrics.Model) {
	ri := result.ReplicationIncoming
	var recvDiff string
	if d, ok := lsnDiff(ri.ReceivedLSN, ri.ReceiveStartLSN); ok && d > 0 {
		recvDiff = ", " + humanize.IBytes(uint64(d))
	}

	fmt.Fprintf(fd, `
Incoming Replication Stats:
    Status:              %s
    Received LSN:        %s (started at %s%s)
    Timeline:            %d (was %d at start)
    Latency:             %s
    Replication Slot:    %s
`,
		ri.Status,
		ri.ReceivedLSN, ri.ReceiveStartLSN, recvDiff,
		ri.ReceivedTLI, ri.ReceiveStartTLI,
		fmtMicros(ri.Latency),
		ri.SlotName)
}

func reportReplicationOut(fd io.Writer, result *pgmetrics.Model) {
	routs := result.ReplicationOutgoing
	fmt.Fprintf(fd, `
Outgoing Replication Stats:`)
	for i, r := range routs {
		var sp string
		if r.SyncPriority != -1 {
			sp = strconv.Itoa(r.SyncPriority)
		}
		fmt.Fprintf(fd, `
    Destination #%d:
      User:              %s
      Application:       %s
      Client Address:    %s
      State:             %s
      Started At:        %s
      Sent LSN:          %s
      Written Until:     %s%s
      Flushed Until:     %s%s
      Replayed Until:    %s%s
      Sync Priority:     %s
      Sync State:        %s`,
			i+1,
			r.RoleName,
			r.ApplicationName,
			r.ClientAddr,
			r.State,
			fmtTimeAndSince(r.BackendStart),
			r.SentLSN,
			r.WriteLSN, fmtLag(r.SentLSN, r.WriteLSN, "write"),
			r.FlushLSN, fmtLag(r.WriteLSN, r.FlushLSN, "flush"),
			r.ReplayLSN, fmtLag(r.FlushLSN, r.ReplayLSN, "replay"),
			sp,
			r.SyncState,
		)
	}
	fmt.Fprintln(fd)
}

func reportReplicationSlots(fd io.Writer, result *pgmetrics.Model, version int) {
	var phy, log int
	for _, r := range result.ReplicationSlots {
		if r.SlotType == "physical" {
			phy++
		} else {
			log++
		}
	}
	if phy > 0 {
		fmt.Fprintf(fd, `
Physical Replication Slots:
`)
		var tw tableWriter
		cols := []interface{}{"Name", "Active", "Oldest Txn ID", "Restart LSN"}
		if version >= 100000 {
			cols = append(cols, "Temporary")
		}
		tw.add(cols...)
		for _, r := range result.ReplicationSlots {
			if r.SlotType != "physical" {
				continue
			}
			vals := []interface{}{r.SlotName, fmtYesNo(r.Active),
				fmtIntZero(r.Xmin), r.RestartLSN}
			if version >= 100000 {
				vals = append(vals, fmtYesNo(r.Temporary))
			}
			tw.add(vals...)
		}
		tw.write(fd, "    ")
	}
	if log > 0 {
		fmt.Fprintf(fd, `
Logical Replication Slots:
`)
		var tw tableWriter
		cols := []interface{}{"Name", "Plugin", "Database", "Active",
			"Oldest Txn ID", "Restart LSN", "Flushed Until"}
		if version >= 100000 {
			cols = append(cols, "Temporary")
		}
		tw.add(cols...)
		for _, r := range result.ReplicationSlots {
			if r.SlotType != "logical" {
				continue
			}
			vals := []interface{}{r.SlotName, r.Plugin, r.DBName,
				fmtYesNo(r.Active), fmtIntZero(r.Xmin), r.RestartLSN,
				r.ConfirmedFlushLSN}
			if version >= 100000 {
				vals = append(vals, fmtYesNo(r.Temporary))
			}
			tw.add(vals...)
		}
		tw.write(fd, "    ")
	}
}

func reportPublications(fd io.Writer, result *pgmetrics.Model) {
	fmt.Fprintf(fd, `
Logical Replication Publications:`)
	for i, pub := range result.Publications {
		fmt.Fprintf(fd, `
    Publication #%d:
      Name:              %s
      All Tables?        %s
      Propogate:         %s
      Tables:            %d`,
			i+1,
			pub.Name,
			fmtYesNo(pub.AllTables),
			fmtWhat(pub.Insert, pub.Update, pub.Delete),
			pub.TableCount)
	}
	fmt.Fprintln(fd)
}

func fmtWhat(ins, upd, del bool) string {
	parts := make([]string, 0, 3)
	if ins {
		parts = append(parts, "inserts")
	}
	if upd {
		parts = append(parts, "updates")
	}
	if del {
		parts = append(parts, "deletes")
	}
	return strings.Join(parts, ", ")
}

func reportSubscriptions(fd io.Writer, result *pgmetrics.Model) {
	fmt.Fprintf(fd, `
Logical Replication Subscriptions:`)
	for i, sub := range result.Subscriptions {
		fmt.Fprintf(fd, `
    Subscription #%d:
      Name:              %s
      Enabled?           %s
      Publications:      %d
      Tables:            %d
      Workers:           %d
      Received Until:    %s
      Latency:           %s`,
			i+1,
			sub.Name,
			fmtYesNo(sub.Enabled),
			sub.PubCount,
			sub.TableCount,
			sub.WorkerCount,
			sub.ReceivedLSN,
			fmtMicros(sub.Latency))
	}
	fmt.Fprintln(fd)
}

func fmtMicros(v int64) string {
	s := (time.Duration(v) * time.Microsecond).String()
	return strings.Replace(s, "µ", "u", -1)
}

// WAL files and archiving
func reportWAL(fd io.Writer, result *pgmetrics.Model) {

	archiveMode := getSetting(result, "archive_mode") == "on"
	fmt.Fprintf(fd, `
WAL Files:
    WAL Archiving?       %s`,
		fmtYesNo(archiveMode),
	)
	if result.WALCount != -1 {
		fmt.Fprintf(fd, `
    WAL Files:           %d`,
			result.WALCount)
	}
	if archiveMode {
		var rate float64
		secs := result.Metadata.At - result.WALArchiving.StatsReset
		if secs > 0 {
			rate = float64(result.WALArchiving.ArchivedCount) / (float64(secs) / 60)
		}
		var rf string
		if result.WALReadyCount > -1 {
			rf = strconv.Itoa(result.WALReadyCount)
		}
		fmt.Fprintf(fd, `
    Ready Files:         %s
    Archive Rate:        %.2f per min
    Last Archived:       %s
    Last Failure:        %s
    Totals:              %d succeeded, %d failed
    Totals Since:        %s`,
			rf,
			rate,
			fmtTimeAndSince(result.WALArchiving.LastArchivedTime),
			fmtTimeAndSince(result.WALArchiving.LastFailedTime),
			result.WALArchiving.ArchivedCount, result.WALArchiving.FailedCount,
			fmtTimeAndSince(result.WALArchiving.StatsReset),
		)
	}
	fmt.Fprintln(fd)
	maxwalk, maxwalv := getMaxWalSize(result)
	var tw1 tableWriter
	tw1.add("Setting", "Value")
	tw1.add("wal_level", getSetting(result, "wal_level"))
	tw1.add("archive_timeout", getSetting(result, "archive_timeout"))
	tw1.add("wal_compression", getSetting(result, "wal_compression"))
	tw1.add(maxwalk, maxwalv)
	tw1.add("min_wal_size", getSettingBytes(result, "min_wal_size", 16*1024*1024)) //getMinWalSize(result))
	tw1.add("checkpoint_timeout", getSetting(result, "checkpoint_timeout"))
	tw1.add("full_page_writes", getSetting(result, "full_page_writes"))
	tw1.add("wal_keep_segments", getSetting(result, "wal_keep_segments"))
	tw1.write(fd, "    ")
}

func reportBGWriter(fd io.Writer, result *pgmetrics.Model) {

	bgw := result.BGWriter
	blkSize := getBlockSize(result)
	var rate float64
	secs := result.Metadata.At - bgw.StatsReset
	ncps := bgw.CheckpointsTimed + bgw.CheckpointsRequested
	if secs > 0 {
		rate = float64(ncps) / (float64(secs) / 60)
	}
	totBuffers := bgw.BuffersCheckpoint + bgw.BuffersClean + bgw.BuffersBackend
	var pctSched, pctReq, avgWrite, rateBuffers float64
	if ncps > 0 {
		ncpsf := float64(ncps)
		pctSched = 100 * float64(bgw.CheckpointsTimed) / ncpsf
		pctReq = 100 * float64(bgw.CheckpointsRequested) / ncpsf
		avgWrite = float64(bgw.BuffersCheckpoint) * float64(blkSize) / ncpsf
	}
	if secs > 0 {
		rateBuffers = float64(totBuffers) / float64(secs)
	}
	var pctBufCP, pctBufBGW, pctBufBE float64
	if totBuffers > 0 {
		totBuffersf := float64(totBuffers)
		pctBufCP = 100 * float64(bgw.BuffersCheckpoint) / totBuffersf
		pctBufBGW = 100 * float64(bgw.BuffersClean) / totBuffersf
		pctBufBE = 100 * float64(bgw.BuffersBackend) / totBuffersf
	}
	fmt.Fprintf(fd, `
BG Writer:
    Checkpoint Rate:     %.2f per min
    Average Write:       %s per checkpoint
    Total Checkpoints:   %d sched (%.1f%%) + %d req (%.1f%%) = %d
    Total Write:         %s, @ %s per sec
    Buffers Allocated:   %d (%s)
    Buffers Written:     %d chkpt (%.1f%%) + %d bgw (%.1f%%) + %d be (%.1f%%)
    Clean Scan Stops:    %d
    BE fsyncs:           %d
    Counts Since:        %s
`,
		rate,
		humanize.IBytes(uint64(avgWrite)),
		bgw.CheckpointsTimed, pctSched,
		bgw.CheckpointsRequested, pctReq, ncps,
		humanize.IBytes(uint64(blkSize)*uint64(totBuffers)),
		humanize.IBytes(uint64(float64(blkSize)*rateBuffers)),
		bgw.BuffersAlloc, humanize.IBytes(uint64(blkSize)*uint64(bgw.BuffersAlloc)),
		bgw.BuffersCheckpoint, pctBufCP,
		bgw.BuffersClean, pctBufBGW,
		bgw.BuffersBackend, pctBufBE,
		bgw.MaxWrittenClean, bgw.BuffersBackendFsync,
		fmtTimeAndSince(bgw.StatsReset),
	)

	var tw tableWriter
	tw.add("Setting", "Value")
	tw.add("bgwriter_delay", getSetting(result, "bgwriter_delay")+" msec")
	tw.add("bgwriter_flush_after", getSettingBytes(result, "bgwriter_flush_after", uint64(blkSize)))
	tw.add("bgwriter_lru_maxpages", getSetting(result, "bgwriter_lru_maxpages"))
	tw.add("bgwriter_lru_multiplier", getSetting(result, "bgwriter_lru_multiplier"))
	tw.add("block_size", getSetting(result, "block_size"))
	tw.add("checkpoint_timeout", getSetting(result, "checkpoint_timeout")+" sec")
	tw.add("checkpoint_completion_target", getSetting(result, "checkpoint_completion_target"))
	tw.write(fd, "    ")
}

func isWaitingLock(be *pgmetrics.Backend) bool {
	if be.WaitEventType == "waiting" && be.WaitEvent == "waiting" {
		return true // before v9.6, see collector.getActivity94
	}
	return be.WaitEventType == "Lock"
}

func isWaitingOther(be *pgmetrics.Backend) bool {
	return len(be.WaitEventType) > 0 && be.WaitEventType != "Lock" && be.WaitEventType != "waiting"
}

func reportBackends(fd io.Writer, tooLongSecs uint, result *pgmetrics.Model) {
	n := len(result.Backends)
	max := getSettingInt(result, "max_connections")
	isTooLong := func(be *pgmetrics.Backend) bool {
		return be.XactStart > 0 && result.Metadata.At-be.XactStart > int64(tooLongSecs)
	}
	var waitingLocks, waitingOther, idlexact, toolong int
	for _, be := range result.Backends {
		if isWaitingLock(&be) {
			waitingLocks++
		}
		if isWaitingOther(&be) {
			waitingOther++
		}
		if strings.HasPrefix(be.State, "idle in transaction") {
			idlexact++
		}
		if isTooLong(&be) {
			toolong++
		}
	}

	// header
	fmt.Fprintf(fd, `
Backends:
    Total Backends:      %d (%.1f%% of max %d)
    Problematic:         %d waiting on locks, %d waiting on other, %d xact too long, %d idle in xact`,
		n, 100*safeDiv(int64(n), int64(max)), max,
		waitingLocks, waitingOther, toolong, idlexact,
	)

	// "waiting for locks" backends
	if waitingLocks > 0 {
		fmt.Fprint(fd, `
    Waiting for Locks:
`)
		var tw tableWriter
		tw.add("PID", "User", "App", "Client Addr", "Database", "Wait", "Query Start")
		for _, be := range result.Backends {
			if isWaitingLock(&be) {
				tw.add(be.PID, be.RoleName, be.ApplicationName, be.ClientAddr,
					be.DBName, be.WaitEventType+" / "+be.WaitEvent,
					fmtTime(be.QueryStart))
			}
		}
		tw.write(fd, "      ")
	}

	// "other waiting" backends
	if waitingOther > 0 {
		fmt.Fprint(fd, `
    Other Waiting Backends:
`)
		var tw tableWriter
		tw.add("PID", "User", "App", "Client Addr", "Database", "Wait", "Query Start")
		for _, be := range result.Backends {
			if isWaitingOther(&be) {
				tw.add(be.PID, be.RoleName, be.ApplicationName, be.ClientAddr,
					be.DBName, be.WaitEventType+" / "+be.WaitEvent,
					fmtTime(be.QueryStart))
			}
		}
		tw.write(fd, "      ")
	}

	// long running xacts
	if toolong > 0 {
		fmt.Fprintf(fd, `
    Long Running (>%d sec) Transactions:
`, tooLongSecs)
		var tw tableWriter
		tw.add("PID", "User", "App", "Client Addr", "Database", "Transaction Start")
		for _, be := range result.Backends {
			if isTooLong(&be) {
				tw.add(be.PID, be.RoleName, be.ApplicationName, be.ClientAddr, be.DBName,
					fmtTimeAndSince(be.XactStart))
			}
		}
		tw.write(fd, "      ")
	}

	// idle in xact backends
	if idlexact > 0 {
		fmt.Fprint(fd, `
    Idling in Transaction:
`)
		var tw tableWriter
		tw.add("PID", "User", "App", "Client Addr", "Database", "Aborted?", "State Change")
		for _, be := range result.Backends {
			if strings.HasPrefix(be.State, "idle in transaction") {
				tw.add(be.PID, be.RoleName, be.ApplicationName, be.ClientAddr,
					be.DBName, fmtYesNo(strings.Contains(be.State, "aborted")),
					fmtTime(be.StateChange))
			}
		}
		tw.write(fd, "      ")
	}

	if waitingOther+waitingLocks+idlexact+toolong == 0 {
		fmt.Fprintln(fd)
	}
}

func reportVacuumProgress(fd io.Writer, result *pgmetrics.Model) {
	fmt.Fprint(fd, `
Vacuum Progress:`)
	if len(result.VacuumProgress) > 0 {
		for i, v := range result.VacuumProgress {
			sp := fmt.Sprintf("%d of %d (%.1f%% complete)", v.HeapBlksScanned,
				v.HeapBlksTotal, 100*safeDiv(v.HeapBlksScanned, v.HeapBlksTotal))
			fmt.Fprintf(fd, `
    Vacuum Process #%d:
      Phase:             %s
      Database:          %s
      Table:             %s
      Scan Progress:     %s
      Heap Blks Vac'ed:  %d of %d
      Idx Vac Cycles:    %d
      Dead Tuples:       %d
      Dead Tuples Max:   %d`,
				i+1,
				v.Phase,
				v.DBName,
				v.TableName,
				sp,
				v.HeapBlksVacuumed, v.HeapBlksTotal,
				v.IndexVacuumCount,
				v.NumDeadTuples,
				v.MaxDeadTuples,
			)
		}
	} else {
		fmt.Fprint(fd, `
    No manual or auto vacuum jobs in progress.`)
	}
	fmt.Fprintln(fd)

	// settings
	var tw tableWriter
	add := func(s string) { tw.add(s, getSetting(result, s)) }
	tw.add("Setting", "Value")
	tw.add("maintenance_work_mem", getSettingBytes(result, "maintenance_work_mem", 1024))
	add("autovacuum")
	add("autovacuum_analyze_threshold")
	add("autovacuum_vacuum_threshold")
	add("autovacuum_freeze_max_age")
	add("autovacuum_max_workers")
	tw.add("autovacuum_naptime", getSetting(result, "autovacuum_naptime")+" sec")
	add("vacuum_freeze_min_age")
	add("vacuum_freeze_table_age")
	tw.write(fd, "    ")
}

func reportRoles(fd io.Writer, result *pgmetrics.Model) {
	fmt.Fprint(fd, `
Roles:
`)
	var tw tableWriter
	tw.add("Name", "Login", "Repl", "Super", "Creat Rol", "Creat DB", "Bypass RLS", "Inherit", "Expires", "Member Of")
	for _, r := range result.Roles {
		tw.add(
			r.Name,
			fmtYesBlank(r.Rolcanlogin),
			fmtYesBlank(r.Rolreplication),
			fmtYesBlank(r.Rolsuper),
			fmtYesBlank(r.Rolcreaterole),
			fmtYesBlank(r.Rolcreatedb),
			fmtYesBlank(r.Rolbypassrls),
			fmtYesBlank(r.Rolinherit),
			fmtTime(r.Rolvaliduntil),
			strings.Join(r.MemberOf, ", "),
		)
	}
	tw.write(fd, "    ")
}

func reportTablespaces(fd io.Writer, result *pgmetrics.Model) {
	fmt.Fprint(fd, `
Tablespaces:
`)
	var tw tableWriter
	if result.Metadata.Local {
		tw.add("Name", "Owner", "Location", "Size", "Disk Used", "Inode Used")
	} else {
		tw.add("Name", "Owner", "Location", "Size")
	}
	for _, t := range result.Tablespaces {
		var s, du, iu string
		if t.Size != -1 {
			s = humanize.IBytes(uint64(t.Size))
		}
		if result.Metadata.Local && t.DiskUsed > 0 && t.DiskTotal > 0 {
			du = fmt.Sprintf("%s (%.1f%%) of %s",
				humanize.IBytes(uint64(t.DiskUsed)),
				100*safeDiv(t.DiskUsed, t.DiskTotal),
				humanize.IBytes(uint64(t.DiskTotal)))
		}
		if result.Metadata.Local && t.InodesUsed > 0 && t.InodesTotal > 0 {
			iu = fmt.Sprintf("%d (%.1f%%) of %d",
				t.InodesUsed,
				100*safeDiv(t.InodesUsed, t.InodesTotal),
				t.InodesTotal)
		}
		if (t.Name == "pg_default" || t.Name == "pg_global") && t.Location != "" {
			t.Location = "$PGDATA = " + t.Location
		}
		if result.Metadata.Local {
			tw.add(t.Name, t.Owner, t.Location, s, du, iu)
		} else {
			tw.add(t.Name, t.Owner, t.Location, s)
		}
	}
	tw.write(fd, "    ")
}

func getTablespaceName(oid int, result *pgmetrics.Model) string {
	for _, t := range result.Tablespaces {
		if t.OID == oid {
			return t.Name
		}
	}
	return ""
}

func getRoleName(oid int, result *pgmetrics.Model) string {
	for _, r := range result.Roles {
		if r.OID == oid {
			return r.Name
		}
	}
	return ""
}

func fmtConns(d *pgmetrics.Database) string {
	if d.DatConnLimit < 0 {
		return fmt.Sprintf("%d (no max limit)", d.NumBackends)
	}
	pct := 100 * safeDiv(int64(d.NumBackends), int64(d.DatConnLimit))
	return fmt.Sprintf("%d (%.1f%%) of %d", d.NumBackends, pct, d.DatConnLimit)
}

func reportDatabases(fd io.Writer, result *pgmetrics.Model) {
	for i, d := range result.Databases {
		fmt.Fprintf(fd, `
Database #%d:
    Name:                %s
    Owner:               %s
    Tablespace:          %s
    Connections:         %s
    Frozen Xid Age:      %d
    Transactions:        %d (%.1f%%) commits, %d (%.1f%%) rollbacks
    Cache Hits:          %.1f%%
    Rows Changed:        ins %.1f%%, upd %.1f%%, del %.1f%%
    Total Temp:          %s in %d files
    Problems:            %d deadlocks, %d conflicts
    Totals Since:        %s`,
			i+1,
			d.Name,
			getRoleName(d.DatDBA, result),
			getTablespaceName(d.DatTablespace, result),
			fmtConns(&d),
			d.AgeDatFrozenXid,
			d.XactCommit, 100*safeDiv(d.XactCommit, d.XactCommit+d.XactRollback),
			d.XactRollback, 100*safeDiv(d.XactRollback, d.XactCommit+d.XactRollback),
			100*safeDiv(d.BlksHit, d.BlksHit+d.BlksRead),
			100*safeDiv(d.TupInserted, d.TupInserted+d.TupUpdated+d.TupDeleted),
			100*safeDiv(d.TupUpdated, d.TupInserted+d.TupUpdated+d.TupDeleted),
			100*safeDiv(d.TupDeleted, d.TupInserted+d.TupUpdated+d.TupDeleted),
			humanize.IBytes(uint64(d.TempBytes)), d.TempFiles,
			d.Deadlocks, d.Conflicts,
			fmtTimeAndSince(d.StatsReset),
		)
		if d.Size != -1 {
			fmt.Fprintf(fd, `
    Size:                %s`, humanize.IBytes(uint64(d.Size)))
		}
		fmt.Fprintln(fd)

		gap := false
		if sqs := filterSequencesByDB(result, d.Name); len(sqs) > 0 {
			fmt.Fprint(fd, `    Sequences:
`)
			var tw tableWriter
			tw.add("Sequence", "Cache Hits")
			for _, sq := range sqs {
				tw.add(sq.Name, fmtPct(sq.BlksHit, sq.BlksHit+sq.BlksRead))
			}
			tw.write(fd, "      ")
			gap = true
		}

		if ufs := filterUserFuncsByDB(result, d.Name); len(ufs) > 0 {
			if gap {
				fmt.Fprintln(fd)
			}
			fmt.Fprint(fd, `    Tracked Functions:
`)
			var tw tableWriter
			tw.add("Function", "Calls", "Time (self)", "Time (self+children)")
			for _, uf := range ufs {
				tw.add(
					uf.Name,
					uf.Calls,
					time.Duration(uf.SelfTime*1e6),
					time.Duration(uf.TotalTime*1e6),
				)
			}
			tw.write(fd, "      ")
			gap = true
		}

		if exts := filterExtensionsByDB(result, d.Name); len(exts) > 0 {
			if gap {
				fmt.Fprintln(fd)
			}
			fmt.Fprint(fd, `    Installed Extensions:
`)
			var tw tableWriter
			tw.add("Name", "Version", "Comment")
			for _, ext := range exts {
				tw.add(ext.Name, ext.InstalledVersion, ext.Comment)
			}
			tw.write(fd, "      ")
			gap = true
		}

		if dts := filterTriggersByDB(result, d.Name); len(dts) > 0 {
			if gap {
				fmt.Fprintln(fd)
			}
			fmt.Fprint(fd, `    Disabled Triggers:
`)
			var tw tableWriter
			tw.add("Name", "Table", "Procedure")
			for _, dt := range dts {
				tw.add(
					dt.Name,
					dt.SchemaName+"."+dt.TableName,
					dt.ProcName,
				)
			}
			tw.write(fd, "      ")
			gap = true
		}

		if ss := filterStatementsByDB(result, d.Name); len(ss) > 0 {
			if gap {
				fmt.Fprintln(fd)
			}
			fmt.Fprint(fd, `    Slow Queries:
`)
			var tw tableWriter
			tw.add("Calls", "Avg Time", "Total Time", "Rows/Call", "Query")
			for _, s := range ss {
				var rpc int64
				if s.Calls > 0 {
					rpc = s.Rows / s.Calls
				}
				tw.add(
					s.Calls,
					prepmsec(s.TotalTime/float64(s.Calls)),
					prepmsec(s.TotalTime),
					rpc,
					prepQ(s.Query),
				)
			}
			tw.write(fd, "      ")
		}
	}
}

const stmtSQLDisplayLength = 50

func prepQ(s string) string {
	if len(s) > stmtSQLDisplayLength {
		s = s[:stmtSQLDisplayLength]
	}
	return strings.Map(smap, s)
}

func smap(r rune) rune {
	if r == '\r' || r == '\n' || r == '\t' {
		return ' '
	}
	return r
}

func prepmsec(ms float64) string {
	return time.Duration(1e6 * ms).Truncate(time.Millisecond).String()
}

func filterSequencesByDB(result *pgmetrics.Model, db string) (out []*pgmetrics.Sequence) {
	for i := range result.Sequences {
		s := &result.Sequences[i]
		if s.DBName == db {
			out = append(out, s)
		}
	}
	return
}

func filterUserFuncsByDB(result *pgmetrics.Model, db string) (out []*pgmetrics.UserFunction) {
	for i := range result.UserFunctions {
		uf := &result.UserFunctions[i]
		if uf.DBName == db {
			out = append(out, uf)
		}
	}
	return
}

func filterExtensionsByDB(result *pgmetrics.Model, db string) (out []*pgmetrics.Extension) {
	for i := range result.Extensions {
		e := &result.Extensions[i]
		if e.DBName == db {
			out = append(out, e)
		}
	}
	return
}

func filterTriggersByDB(result *pgmetrics.Model, db string) (out []*pgmetrics.Trigger) {
	for i := range result.DisabledTriggers {
		t := &result.DisabledTriggers[i]
		if t.DBName == db {
			out = append(out, t)
		}
	}
	return
}

func filterStatementsByDB(result *pgmetrics.Model, db string) (out []*pgmetrics.Statement) {
	for i := range result.Statements {
		s := &result.Statements[i]
		if s.DBName == db {
			out = append(out, s)
		}
	}
	return
}

func filterTablesByDB(result *pgmetrics.Model, db string) (out []*pgmetrics.Table) {
	for i := range result.Tables {
		t := &result.Tables[i]
		if t.DBName == db {
			out = append(out, t)
		}
	}
	return
}

func fmtPct(a, b int64) string {
	if b == 0 {
		return ""
	}
	return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
}

func fmtCountAndTime(n, last int64) string {
	if n == 0 || last == 0 {
		return "never"
	}
	return fmt.Sprintf("%d, last %s", n, fmtSince(last))
}

func filterIndexesByTable(result *pgmetrics.Model, db, schema, table string) (out []*pgmetrics.Index) {
	for i := range result.Indexes {
		idx := &result.Indexes[i]
		if idx.DBName == db && idx.SchemaName == schema && idx.TableName == table {
			out = append(out, idx)
		}
	}
	return
}

func reportTables(fd io.Writer, result *pgmetrics.Model) {
	for _, db := range result.Metadata.CollectedDBs {
		tables := filterTablesByDB(result, db)
		if len(tables) == 0 {
			continue
		}
		for i, t := range tables {
			nTup := t.NLiveTup + t.NDeadTup
			nTupChanged := t.NTupIns + t.NTupUpd + t.NTupDel
			attrs := tableAttrs(t)
			fmt.Fprintf(fd, `
Table #%d in "%s":
    Name:                %s.%s.%s`,
				i+1,
				db,
				db, t.SchemaName, t.Name)
			if len(attrs) > 0 {
				fmt.Fprintf(fd, `
    Attributes:          %s`, attrs)
			}
			if len(t.TablespaceName) > 0 {
				fmt.Fprintf(fd, `
    Tablespace:          %s`, t.TablespaceName)
			}
			fmt.Fprintf(fd, `
    Columns:             %d
    Manual Vacuums:      %s
    Manual Analyze:      %s
    Auto Vacuums:        %s
    Auto Analyze:        %s
    Post-Analyze:        %.1f%% est. rows modified
    Row Estimate:        %.1f%% live of total %d
    Rows Changed:        ins %.1f%%, upd %.1f%%, del %.1f%%
    HOT Updates:         %.1f%% of all updates
    Seq Scans:           %d, %.1f rows/scan
    Idx Scans:           %d, %.1f rows/scan
    Cache Hits:          %.1f%% (idx=%.1f%%)`,
				t.RelNAtts,
				fmtCountAndTime(t.VacuumCount, t.LastVacuum),
				fmtCountAndTime(t.AnalyzeCount, t.LastAnalyze),
				fmtCountAndTime(t.AutovacuumCount, t.LastAutovacuum),
				fmtCountAndTime(t.AutoanalyzeCount, t.LastAutoanalyze),
				100*safeDiv(t.NModSinceAnalyze, nTup),
				100*safeDiv(t.NLiveTup, nTup), nTup,
				100*safeDiv(t.NTupIns, nTupChanged),
				100*safeDiv(t.NTupHotUpd, nTupChanged),
				100*safeDiv(t.NTupDel, nTupChanged),
				100*safeDiv(t.NTupHotUpd, t.NTupUpd),
				t.SeqScan, safeDiv(t.SeqTupRead, t.SeqScan),
				t.IdxScan, safeDiv(t.IdxTupFetch, t.IdxScan),
				100*safeDiv(t.HeapBlksHit+t.ToastBlksHit+t.TidxBlksHit,
					t.HeapBlksHit+t.HeapBlksRead+
						t.ToastBlksHit+t.ToastBlksRead+
						t.TidxBlksHit+t.TidxBlksRead),
				100*safeDiv(t.IdxBlksHit, t.IdxBlksHit+t.IdxBlksRead),
			)
			if t.Size != -1 {
				fmt.Fprintf(fd, `
    Size:                %s`, humanize.IBytes(uint64(t.Size)))
			}
			if t.Bloat != -1 {
				if t.Size != -1 {
					fmt.Fprintf(fd, `
    Bloat:               %s (%.1f%%)`,
						humanize.IBytes(uint64(t.Bloat)),
						100*safeDiv(t.Bloat, t.Size))
				} else {
					fmt.Fprintf(fd, `
    Bloat:               %s`, humanize.IBytes(uint64(t.Bloat)))
				}
			}
			fmt.Fprintln(fd)

			idxs := filterIndexesByTable(result, db, t.SchemaName, t.Name)
			if len(idxs) == 0 {
				continue
			}
			var tw tableWriter
			tw.add("Index", "Type", "Size", "Bloat", "Cache Hits", "Scans", "Rows Read/Scan", "Rows Fetched/Scan")
			for _, idx := range idxs {
				var sz, bloat string
				if idx.Size != -1 {
					sz = humanize.IBytes(uint64(idx.Size))
				}
				if idx.Bloat != -1 {
					if idx.Size != -1 {
						bloat = fmt.Sprintf("%s (%.1f%%)",
							humanize.IBytes(uint64(idx.Bloat)),
							100*safeDiv(idx.Bloat, idx.Size))
					} else {
						bloat = humanize.IBytes(uint64(idx.Bloat))
					}
				}
				tw.add(
					idx.Name,
					idx.AMName,
					sz,
					bloat,
					fmtPct(idx.IdxBlksHit, idx.IdxBlksHit+idx.IdxBlksRead),
					idx.IdxScan,
					fmt.Sprintf("%.1f", safeDiv(idx.IdxTupRead, idx.IdxScan)),
					fmt.Sprintf("%.1f", safeDiv(idx.IdxTupFetch, idx.IdxScan)),
				)
			}
			tw.write(fd, "    ")
		}
	}
}

func tableAttrs(t *pgmetrics.Table) string {
	var parts []string
	if t.RelPersistence == "u" {
		parts = append(parts, "unlogged")
	} else if t.RelPersistence == "t" {
		parts = append(parts, "temporary")
	}
	if t.RelKind == "m" {
		parts = append(parts, "materialized view")
	} else if t.RelKind == "p" {
		parts = append(parts, "partition parent")
	}
	if t.RelIsPartition {
		parts = append(parts, "partition")
	}
	return strings.Join(parts, ", ")
}

func reportSystem(fd io.Writer, result *pgmetrics.Model) {
	s := result.System
	fmt.Fprintf(fd, `
System Information:
    Hostname:            %s
    CPU Cores:           %d x %s
    Load Average:        %.2f
    Memory:              used=%s, free=%s, buff=%s, cache=%s
    Swap:                used=%s, free=%s
`,
		s.Hostname,
		s.NumCores, s.CPUModel,
		s.LoadAvg,
		humanize.IBytes(uint64(s.MemUsed)),
		humanize.IBytes(uint64(s.MemFree)),
		humanize.IBytes(uint64(s.MemBuffers)),
		humanize.IBytes(uint64(s.MemCached)),
		humanize.IBytes(uint64(s.SwapUsed)),
		humanize.IBytes(uint64(s.SwapFree)),
	)
	var tw tableWriter
	tw.add("Setting", "Value")
	add := func(k string) { tw.add(k, getSetting(result, k)) }
	addBytes := func(k string, f uint64) { tw.add(k, getSettingBytes(result, k, f)) }
	addBytes("shared_buffers", 8192)
	addBytes("work_mem", 1024)
	addBytes("maintenance_work_mem", 1024)
	addBytes("temp_buffers", 8192)
	if v := getSetting(result, "autovacuum_work_mem"); v == "-1" {
		tw.add("autovacuum_work_mem", v)
	} else {
		addBytes("autovacuum_work_mem", 1024)
	}
	if v := getSetting(result, "temp_file_limit"); v == "-1" {
		tw.add("temp_file_limit", v)
	} else {
		addBytes("temp_file_limit", 1024)
	}
	add("max_worker_processes")
	add("autovacuum_max_workers")
	add("max_parallel_workers_per_gather")
	add("effective_io_concurrency")
	tw.write(fd, "    ")
}

//------------------------------------------------------------------------------

func fmtTime(at int64) string {
	if at == 0 {
		return ""
	}
	return time.Unix(at, 0).Format("2 Jan 2006 3:04:05 PM")
}

func fmtTimeDef(at int64, def string) string {
	if at == 0 {
		return def
	}
	return time.Unix(at, 0).Format("2 Jan 2006 3:04:05 PM")
}

func fmtTimeAndSince(at int64) string {
	if at == 0 {
		return ""
	}
	t := time.Unix(at, 0)
	return fmt.Sprintf("%s (%s)", t.Format("2 Jan 2006 3:04:05 PM"),
		humanize.Time(t))
}

func fmtTimeAndSinceDef(at int64, def string) string {
	if at == 0 {
		return def
	}
	t := time.Unix(at, 0)
	return fmt.Sprintf("%s (%s)", t.Format("2 Jan 2006 3:04:05 PM"),
		humanize.Time(t))
}

func fmtSince(at int64) string {
	if at == 0 {
		return "never"
	}
	return humanize.Time(time.Unix(at, 0))
}

func fmtYesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func fmtYesBlank(v bool) string {
	if v {
		return "yes"
	}
	return ""
}

func fmtSeconds(s string) string {
	v, err := strconv.Atoi(s)
	if err != nil {
		return s
	}
	return (time.Duration(v) * time.Second).String()
}

func fmtLag(a, b, qual string) string {
	if len(qual) > 0 && !strings.HasSuffix(qual, " ") {
		qual += " "
	}
	if d, ok := lsnDiff(a, b); ok {
		if d == 0 {
			return " (no " + qual + "lag)"
		}
		return fmt.Sprintf(" (%slag = %s)", qual, humanize.IBytes(uint64(d)))
	}
	return ""
}

func fmtIntZero(i int) string {
	if i == 0 {
		return ""
	}
	return strconv.Itoa(i)
}

func getSetting(result *pgmetrics.Model, key string) string {
	if s, ok := result.Settings[key]; ok {
		return s.Setting
	}
	return ""
}

func getSettingInt(result *pgmetrics.Model, key string) int {
	s := getSetting(result, key)
	if len(s) == 0 {
		return 0
	}
	val, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return val
}

func getSettingBytes(result *pgmetrics.Model, key string, factor uint64) string {
	s := getSetting(result, key)
	if len(s) == 0 {
		return s
	}
	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil || val <= 0 {
		return s
	}
	return s + " (" + humanize.IBytes(val*factor) + ")"
}

func safeDiv(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func lsn2int(s string) int64 {
	if len(s) == 0 {
		return -1
	}
	if pos := strings.IndexByte(s, '/'); pos >= 0 {
		val1, err1 := strconv.ParseUint(s[:pos], 16, 64)
		val2, err2 := strconv.ParseUint(s[pos+1:], 16, 64)
		if err1 != nil || err2 != nil {
			return -1
		}
		return int64(val1<<32 | val2)
	}
	return -1
}

func lsnDiff(a, b string) (int64, bool) {
	va := lsn2int(a)
	vb := lsn2int(b)
	if va == -1 || vb == -1 {
		return -1, false
	}
	return va - vb, true
}

func getBlockSize(result *pgmetrics.Model) int {
	s := getSetting(result, "block_size")
	if len(s) == 0 {
		return 8192
	}
	v, err := strconv.Atoi(s)
	if err != nil || v == 0 {
		return 8192
	}
	return v
}

func getVersion(result *pgmetrics.Model) int {
	s := getSetting(result, "server_version_num")
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

func getMaxWalSize(result *pgmetrics.Model) (string, string) {
	var key string
	if version := getVersion(result); version >= 90500 {
		key = "max_wal_size"
	} else {
		key = "checkpoint_segments"
	}
	return key, getSettingBytes(result, key, 16*1024*1024)
}

//------------------------------------------------------------------------------

type tableWriter struct {
	data [][]string
}

func (t *tableWriter) add(cols ...interface{}) {
	row := make([]string, len(cols))
	for i, c := range cols {
		row[i] = fmt.Sprintf("%v", c)
	}
	t.data = append(t.data, row)
}

func (t *tableWriter) clear() {
	t.data = nil
}

func (t *tableWriter) cols() int {
	n := 0
	for _, row := range t.data {
		if n < len(row) {
			n = len(row)
		}
	}
	return n
}

func (t *tableWriter) write(fd io.Writer, pfx string) {
	if len(t.data) == 0 {
		return
	}
	ncols := t.cols()
	if ncols == 0 {
		return
	}
	// calculate widths
	widths := make([]int, ncols)
	for _, row := range t.data {
		for c, col := range row {
			w := len(col)
			if widths[c] < w {
				widths[c] = w
			}
		}
	}
	// print line
	line := func() {
		fmt.Fprintf(fd, "%s+", pfx)
		for _, w := range widths {
			fmt.Fprint(fd, strings.Repeat("-", w+2))
			fmt.Fprintf(fd, "+")
		}
		fmt.Fprintln(fd)
	}
	line()
	for i, row := range t.data {
		if i == 1 {
			line()
		}
		fmt.Fprintf(fd, "%s|", pfx)
		for c, col := range row {
			fmt.Fprintf(fd, " %*s |", widths[c], col)
		}
		fmt.Fprintln(fd)
	}
	line()
}
