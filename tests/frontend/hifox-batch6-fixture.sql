-- T02 controlled historical rows. Load BEFORE migration 121 so unknown DONE
-- really crosses the nullable-column migration without a fabricated timestamp.
\set ON_ERROR_STOP on
BEGIN;
SELECT openfox_private.set_tenant_context(
 '00000000-0000-0000-0000-0000000000a1',
 '00000000-0000-0000-0000-000000000001', 1, 'user_request');
INSERT INTO openfox.task_types (organization_id, space_id, id, name)
VALUES ('00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000aa',
 '00000000-0000-0000-0000-000000006021','T02 browser type');
INSERT INTO openfox.workflow_statuses (organization_id,space_id,task_type_id,status_id,position,is_default)
SELECT organization_id,space_id,'00000000-0000-0000-0000-000000006021',status_id,position,is_default
FROM openfox.workflow_statuses WHERE task_type_id='00000000-0000-0000-0000-000000000111';
INSERT INTO openfox.tasks (organization_id,space_id,type_id,id,sequence,title,status_id,start_date,due_date)
SELECT '00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000aa',
 '00000000-0000-0000-0000-000000006021', seed.id::uuid,seed.seq,seed.title,
 (SELECT id FROM openfox.task_statuses WHERE organization_id='00000000-0000-0000-0000-0000000000a1'
   AND category=seed.category AND archived_at IS NULL ORDER BY position,id LIMIT 1), current_date,current_date+1
FROM (VALUES
 ('00000000-0000-0000-0000-000000006031',201,'T02 unknown historical completion','DONE'),
 ('00000000-0000-0000-0000-000000006032',202,'T02 eight days ago','DONE'),
 ('00000000-0000-0000-0000-000000006033',203,'T02 thirty-one days ago','DONE'),
 ('00000000-0000-0000-0000-000000006034',204,'T02 cancelled task','CANCELLED'),
 ('00000000-0000-0000-0000-000000006035',205,'T02 duplicate task','DUPLICATE'),
 ('00000000-0000-0000-0000-000000006036',206,'T02 active task','TODO')
) AS seed(id,seq,title,category);
UPDATE openfox.spaces SET next_task_number=400 WHERE id='00000000-0000-0000-0000-0000000000aa';
COMMIT;
