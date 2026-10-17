package com.guanjin.sdk.agent;

public final class ReportResponse {

    public String snapshotId;
    public long storedVersion;

    public ReportResponse() {
    }

    public ReportResponse(String snapshotId, long storedVersion) {
        this.snapshotId = snapshotId;
        this.storedVersion = storedVersion;
    }
}
