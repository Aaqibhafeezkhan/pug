-- +goose Up

-- Nothing reads the session rollup since v0.0.35. Drop the MV first: a missing
-- target table under a live MV fails every events insert.
DROP VIEW IF EXISTS dashboard_session_rollup_mv;
DROP TABLE IF EXISTS dashboard_session_rollup;

-- +goose Down

-- Recreates the table and MV as 013 left them, empty: nothing is backfilled.
CREATE TABLE IF NOT EXISTS dashboard_session_rollup (
    project_id                  String,
    kind                        LowCardinality(String),
    session_id                  UUID,
    start_state                 AggregateFunction(min, DateTime64(3)),
    end_state                   AggregateFunction(max, DateTime64(3)),
    event_count_state           AggregateFunction(count),
    entry_url_state             AggregateFunction(argMin, String, DateTime64(3)),
    exit_url_state              AggregateFunction(argMax, String, DateTime64(3)),
    entry_country_state         AggregateFunction(argMin, String, DateTime64(3)),
    exit_country_state          AggregateFunction(argMax, String, DateTime64(3)),
    entry_region_state          AggregateFunction(argMin, String, DateTime64(3)),
    exit_region_state           AggregateFunction(argMax, String, DateTime64(3)),
    entry_city_state            AggregateFunction(argMin, String, DateTime64(3)),
    exit_city_state             AggregateFunction(argMax, String, DateTime64(3)),
    entry_os_state              AggregateFunction(argMin, String, DateTime64(3)),
    exit_os_state               AggregateFunction(argMax, String, DateTime64(3)),
    entry_browser_state         AggregateFunction(argMin, String, DateTime64(3)),
    exit_browser_state          AggregateFunction(argMax, String, DateTime64(3)),
    entry_device_state          AggregateFunction(argMin, String, DateTime64(3)),
    exit_device_state           AggregateFunction(argMax, String, DateTime64(3)),
    entry_platform_state        AggregateFunction(argMin, String, DateTime64(3)),
    exit_platform_state         AggregateFunction(argMax, String, DateTime64(3)),
    entry_utm_source_state      AggregateFunction(argMin, String, DateTime64(3)),
    exit_utm_source_state       AggregateFunction(argMax, String, DateTime64(3)),
    entry_utm_medium_state      AggregateFunction(argMin, String, DateTime64(3)),
    exit_utm_medium_state       AggregateFunction(argMax, String, DateTime64(3)),
    entry_utm_campaign_state    AggregateFunction(argMin, String, DateTime64(3)),
    exit_utm_campaign_state     AggregateFunction(argMax, String, DateTime64(3)),
    entry_pathname_state        AggregateFunction(argMin, String, DateTime64(3)),
    exit_pathname_state         AggregateFunction(argMax, String, DateTime64(3)),
    entry_referrer_domain_state AggregateFunction(argMin, String, DateTime64(3)),
    exit_referrer_domain_state  AggregateFunction(argMax, String, DateTime64(3)),
    entry_channel_state         AggregateFunction(argMin, String, DateTime64(3)),
    exit_channel_state          AggregateFunction(argMax, String, DateTime64(3)),
    entry_utm_term_state        AggregateFunction(argMin, String, DateTime64(3)),
    exit_utm_term_state         AggregateFunction(argMax, String, DateTime64(3)),
    entry_utm_content_state     AggregateFunction(argMin, String, DateTime64(3)),
    exit_utm_content_state      AggregateFunction(argMax, String, DateTime64(3)),
    bot                         UInt8
) ENGINE = AggregatingMergeTree
PRIMARY KEY (project_id, kind, session_id)
ORDER BY (project_id, kind, session_id, bot);

CREATE MATERIALIZED VIEW IF NOT EXISTS dashboard_session_rollup_mv
TO dashboard_session_rollup AS
SELECT
    project_id,
    kind,
    session_id,
    minState(occur_time) AS start_state,
    maxState(occur_time) AS end_state,
    countState() AS event_count_state,
    argMinState(url, occur_time) AS entry_url_state,
    argMaxState(url, occur_time) AS exit_url_state,
    argMinState(toString(country), occur_time) AS entry_country_state,
    argMaxState(toString(country), occur_time) AS exit_country_state,
    argMinState(toString(region), occur_time) AS entry_region_state,
    argMaxState(toString(region), occur_time) AS exit_region_state,
    argMinState(city, occur_time) AS entry_city_state,
    argMaxState(city, occur_time) AS exit_city_state,
    argMinState(toString(os), occur_time) AS entry_os_state,
    argMaxState(toString(os), occur_time) AS exit_os_state,
    argMinState(toString(browser), occur_time) AS entry_browser_state,
    argMaxState(toString(browser), occur_time) AS exit_browser_state,
    argMinState(toString(device), occur_time) AS entry_device_state,
    argMaxState(toString(device), occur_time) AS exit_device_state,
    argMinState(toString(platform), occur_time) AS entry_platform_state,
    argMaxState(toString(platform), occur_time) AS exit_platform_state,
    argMinState(toString(utm_source), occur_time) AS entry_utm_source_state,
    argMaxState(toString(utm_source), occur_time) AS exit_utm_source_state,
    argMinState(toString(utm_medium), occur_time) AS entry_utm_medium_state,
    argMaxState(toString(utm_medium), occur_time) AS exit_utm_medium_state,
    argMinState(toString(utm_campaign), occur_time) AS entry_utm_campaign_state,
    argMaxState(toString(utm_campaign), occur_time) AS exit_utm_campaign_state,
    argMinState(pathname, occur_time) AS entry_pathname_state,
    argMaxState(pathname, occur_time) AS exit_pathname_state,
    argMinState(toString(referrer_domain), occur_time) AS entry_referrer_domain_state,
    argMaxState(toString(referrer_domain), occur_time) AS exit_referrer_domain_state,
    argMinState(toString(channel), occur_time) AS entry_channel_state,
    argMaxState(toString(channel), occur_time) AS exit_channel_state,
    argMinState(toString(utm_term), occur_time) AS entry_utm_term_state,
    argMaxState(toString(utm_term), occur_time) AS exit_utm_term_state,
    argMinState(toString(utm_content), occur_time) AS entry_utm_content_state,
    argMaxState(toString(utm_content), occur_time) AS exit_utm_content_state,
    toUInt8(bot) AS bot
FROM (
    SELECT
        project_id,
        session_id,
        '' AS kind,
        occur_time,
        url,
        country,
        region,
        city,
        os,
        browser,
        device,
        platform,
        utm_source,
        utm_medium,
        utm_campaign,
        pathname,
        referrer_domain,
        channel,
        utm_term,
        utm_content,
        bot
    FROM events

    UNION ALL

    SELECT
        project_id,
        session_id,
        kind,
        occur_time,
        url,
        country,
        region,
        city,
        os,
        browser,
        device,
        platform,
        utm_source,
        utm_medium,
        utm_campaign,
        pathname,
        referrer_domain,
        channel,
        utm_term,
        utm_content,
        bot
    FROM events
) AS scoped_events
GROUP BY project_id, kind, session_id, bot;
