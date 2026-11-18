SET ROLE openfox_migrator;
ALTER TABLE openfox.import_jobs
    DROP CONSTRAINT import_jobs_source_valid,
    ADD CONSTRAINT import_jobs_source_valid CHECK
      (source IN ('jira_csv', 'jira_cloud', 'jira_server', 'space_clone', 'teambition_csv', 'pingcode_csv','tapd_excel'));
RESET ROLE;
