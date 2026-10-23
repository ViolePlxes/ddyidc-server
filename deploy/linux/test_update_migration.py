import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('controlled_migration',Path(__file__).with_name('update-migration.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)


class ControlledMigrationTests(unittest.TestCase):
    def fixture(self,folder):
        root=Path(folder);sql=root/'internal/shared/migrations/sql';sql.mkdir(parents=True)
        (root/'tools/architecture').mkdir(parents=True)
        (sql/'001.sql').write_text('CREATE TABLE example(id INT);')
        digest=hashlib.sha256((sql/'001.sql').read_bytes()).hexdigest()
        (root/'tools/architecture/automatic-migrations.json').write_text(json.dumps({'001.sql':{'sha256':digest,'rollbackCompatible':True}}))
        rows={'999_module_seeds_v1':(hashlib.sha256(b'module-seeds-v1').hexdigest(),'complete')}
        return root,digest,rows

    def test_pending_reviewed_and_repeat_are_distinct(self):
        with tempfile.TemporaryDirectory() as folder:
            root,digest,rows=self.fixture(folder)
            self.assertEqual(m.inspect_history(root,rows),['001.sql'])
            rows['001.sql']=(digest,'complete')
            self.assertEqual(m.inspect_history(root,rows),[])

    def test_pending_cannot_hide_dirty_or_modified_history(self):
        with tempfile.TemporaryDirectory() as folder:
            root,digest,rows=self.fixture(folder)
            for row in [(digest,'running'),('0'*64,'complete')]:
                with self.subTest(row=row),self.assertRaises(m.Refused):m.inspect_history(root,{**rows,'001.sql':row})
            rows['999_module_seeds_v1']=('0'*64,'complete')
            with self.assertRaises(m.Refused):m.inspect_history(root,rows)

    def test_unreviewed_and_noncompatible_require_manual_review(self):
        with tempfile.TemporaryDirectory() as folder:
            root,digest,rows=self.fixture(folder)
            (root/'internal/shared/migrations/sql/002.sql').write_text('DROP TABLE important;')
            with self.assertRaises(m.Refused):m.inspect_history(root,rows)
            (root/'internal/shared/migrations/sql/002.sql').unlink()
            (root/'tools/architecture/automatic-migrations.json').write_text(json.dumps({'001.sql':{'sha256':digest,'rollbackCompatible':False}}))
            with self.assertRaises(m.Refused):m.inspect_history(root,rows)

    def test_private_policy_rejects_app_owned_or_linked_host_keys(self):
        with tempfile.TemporaryDirectory() as folder:
            p=Path(folder)/'policy';p.write_text('{}');p.chmod(0o644)
            with self.assertRaises(m.Refused):m.private_file(p)
            p.chmod(0o600)
            link=Path(folder)/'linked';link.symlink_to(p)
            with self.assertRaises(m.Refused):m.private_file(link)

    def test_no_arbitrary_transport_or_credentials_in_repository_url(self):
        for url in ['file:///tmp/repo','ext::sh -c payload','https://user:secret@host/repo','ssh://git@attacker.test/repo.git','ssh://git@codeup.aliyun.com/repo.git;id']:
            with self.subTest(url=url),self.assertRaises(m.Refused):m.validate_policy({'repository':url,'identityFile':'x','knownHostsFile':'x','socket':'/run/mysqld/mysqld.sock'})

    def test_command_failure_does_not_expose_payload(self):
        with self.assertRaises(m.Refused) as error:m.command(['/usr/bin/python3','-c','import sys;print("secret-payload");sys.exit(7)'])
        self.assertNotIn('secret-payload',str(error.exception))

    def test_command_loss_of_lease_terminates_child(self):
        def lost():raise m.Refused('lease lost')
        with self.assertRaises(m.Refused):m.command(['/usr/bin/python3','-c','import time;time.sleep(20)'],guard=lost)

    def test_runtime_verification_passes_only_executable_fd(self):
        with tempfile.TemporaryDirectory() as folder:
            candidate=Path(folder)/'candidate';candidate.write_bytes(b'fixture')
            with mock.patch.object(m,'command') as run,mock.patch.object(m.pwd,'getpwnam',return_value=object()):m.verify_runtime(candidate,lambda:None)
            args,options=run.call_args
            self.assertTrue(args[0][0].startswith('/proc/self/fd/'))
            self.assertEqual(len(options['pass_fds']),1)
            self.assertNotIn('--migration-socket',args[0])

    def test_build_has_dedicated_identity_no_config_and_no_shared_cache(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);source=root/'source';source.mkdir()
            def build(args,**kwargs):
                self.assertIn('--uid=ddyidc-migration-build',args)
                self.assertNotIn('--uid=ddyidc-build',args)
                self.assertIn('GOTOOLCHAIN=auto',args)
                self.assertIn('GOSUMDB=sum.golang.org',args)
                self.assertIn('GOWORK=off',args)
                mounts=[arg for arg in args if arg.startswith('BindReadOnlyPaths=')]
                self.assertEqual(len(mounts),1)
                self.assertIn('/etc/ddyidc-build/resolv.conf:/etc/resolv.conf ',mounts[0])
                self.assertIn(str(source)+':/tmp/ddyidc-migration-source',mounts[0])
                self.assertTrue(any('InaccessiblePaths=/etc/ddyidc' in arg for arg in args))
                (root/'output/candidate').write_bytes(b'\x7fELFtrusted')
            with mock.patch.object(m,'build_network_guard',return_value='127.0.0.53'),mock.patch.object(m,'command',side_effect=build),mock.patch.object(m.subprocess,'run'):
                candidate=m.build_candidate(source,root,'a'*40,lambda:None)
            self.assertEqual(candidate.read_bytes(),b'\x7fELFtrusted')

    def test_builder_rejects_missing_ipv6_uid_firewall_hook(self):
        with mock.patch.object(m,'command'),mock.patch.object(m.pwd,'getpwnam',return_value=type('User',(),{'pw_uid':1234})()),mock.patch.object(m.subprocess,'check_output',side_effect=['-A OUTPUT -m owner --uid-owner 1234 -j DDYIDC_BUILD\n','']):
            with self.assertRaises(m.Refused):m.build_network_guard(lambda:None)

    def test_root_result_is_runtime_readable_and_does_not_follow_link(self):
        if os.geteuid()!=0:self.skipTest('root filesystem boundary test')
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);target=root/'99.result.json';outside=root/'outside';outside.write_text('keep');target.symlink_to(outside)
            with mock.patch.object(m,'RESULTS',root),mock.patch.object(m.pwd,'getpwnam',return_value=type('User',(),{'pw_gid':os.getgid(),'pw_uid':os.getuid()})()):m.atomic_result('99','controlled failure')
            self.assertEqual(outside.read_text(),'keep')
            self.assertFalse(target.is_symlink())
            self.assertEqual(target.stat().st_mode&0o777,0o600)

    def exercise(self, folder, fail=None):
        root=Path(folder);source,digest,rows=self.fixture(root/'release')
        config=root/'config.yaml';config.write_text('MySQL:\n  Host: 127.0.0.1\n  Port: 3306\n  Database: ddyidc\nRedis: {}\n');config.chmod(0o640)
        policy=root/'policy.json';policy.write_text('{"socket":"/run/mysqld/mysqld.sock"}');policy.chmod(0o600)
        results=root/'results';results.mkdir();(results/'pending.plan.json').write_text(json.dumps({'needsMigration':True,'taskId':'99','expectedCommit':'a'*40}))
        work=root/'work';backup=root/'backup';calls=[];barrier=[False]
        class Lease:
            def __init__(self,cfg):pass
            def command(self,*args):return '1'
        def query(socket,sql):
            if sql=='SELECT @@port;':return '3306'
            if 'SELECT target_commit' in sql:return 'a'*40
            if 'UPDATE system.update_tasks' in sql:return ''
            if 'SELECT name,checksum,state' in sql:return '\n'.join(name+'\t'+value[0]+'\t'+value[1] for name,value in rows.items())
            if 'CHECKSUM TABLE' in sql:return 'changed' if fail=='money' and 'migrate' in calls else 'unchanged'
            if 'SELECT SCHEMA_NAME' in sql:return 'billing\nsystem'
            if "SET mode='restore'" in sql:barrier[0]=True;calls.append('barrier');return '1'
            if "SET mode='running'" in sql:barrier[0]=False;calls.append('release');return '1'
            raise AssertionError(sql)
        candidate=root/'candidate';candidate.write_bytes(b'fixture')
        def command(args,**kwargs):
            if 'mysqldump' in args[0]:
                calls.append('backup')
                if fail=='backup':raise m.Refused('backup failed')
                kwargs['output'].write(b'-- fixture\n-- Dump completed on fixture\n')
            elif '--migrate' in args:
                calls.append('migrate')
                if fail=='migrate':raise m.Refused('migration failed')
            elif args[1:]==['stop','ddyidc.service']:calls.append('stop')
            elif args[1:]==['start','ddyidc.service']:calls.append('restart-old')
            elif 'ddyidc-update-helper.service' in args:
                calls.append('handoff');(results/'99.result.json').write_text('{}')
            else:raise AssertionError(args)
        patches=[mock.patch.object(m,'POLICY',policy),mock.patch.object(m,'CONFIG',config),mock.patch.object(m,'RESULTS',results),mock.patch.object(m,'WORK',work),mock.patch.object(m,'BACKUPS',backup),mock.patch.object(m.sys,'argv',['worker']),mock.patch.object(m,'validate_policy'),mock.patch.object(m,'query',side_effect=query),mock.patch.object(m,'export_release',return_value=(source,'a'*40)),mock.patch.object(m,'build_candidate',return_value=candidate),mock.patch.object(m,'verify_runtime',side_effect=lambda *a:calls.append('verify')),mock.patch.object(m,'advance_workspace'),mock.patch.object(m,'command',side_effect=command),mock.patch.object(m,'redis_connection',return_value=Lease({})),mock.patch.object(m.pwd,'getpwnam',return_value=type('User',(),{'pw_gid':os.getgid(),'pw_uid':os.getuid()})())]
        import contextlib
        with contextlib.ExitStack() as stack:
            for patch in patches:stack.enter_context(patch)
            if fail:
                with self.assertRaises(m.Refused):m.main()
            else:m.main()
        return calls,barrier[0],backup,results

    def test_legacy_trigger_dump_removes_only_outer_terminator(self):
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder)/'database.sql'
            trigger=b'/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `t` BEFORE UPDATE ON `x` FOR EACH ROW SET NEW.a=1; */;;\n'
            other=b"INSERT INTO x VALUES ('; */;;');\n"
            path.write_bytes(trigger+other)
            m.normalize_dump_triggers(path)
            self.assertEqual(path.read_bytes(),trigger.replace(b'1; */;;',b'1 */;;')+other)
            self.assertEqual(path.stat().st_mode&0o777,0o600)

    def test_backup_then_stop_migrate_verify_and_handoff(self):
        if os.geteuid()!=0:self.skipTest('root filesystem boundary test')
        with tempfile.TemporaryDirectory() as folder:
            calls,barrier,backup,results=self.exercise(folder)
            self.assertEqual(calls,['barrier','backup','stop','migrate','verify','release','handoff'])
            self.assertFalse(barrier)
            journal=json.loads(next(backup.glob('*/result.json')).read_text())
            self.assertTrue(journal['financialDataUnchanged'])

    def test_backup_failure_keeps_old_service_and_releases_owned_barrier(self):
        if os.geteuid()!=0:self.skipTest('root filesystem boundary test')
        with tempfile.TemporaryDirectory() as folder:
            calls,barrier,backup,results=self.exercise(folder,'backup')
            self.assertNotIn('stop',calls);self.assertNotIn('migrate',calls)
            self.assertFalse(barrier);self.assertIn('restart-old',calls)
            self.assertEqual(json.loads((results/'99.result.json').read_text())['status'],'failed')

    def test_partial_migration_or_money_change_keeps_backup_and_barrier(self):
        if os.geteuid()!=0:self.skipTest('root filesystem boundary test')
        for failure in ['migrate','money']:
            with self.subTest(failure=failure),tempfile.TemporaryDirectory() as folder:
                calls,barrier,backup,results=self.exercise(folder,failure)
                self.assertTrue(barrier);self.assertNotIn('handoff',calls);self.assertNotIn('restart-old',calls)
                self.assertTrue(next(backup.glob('*/database.sql')).is_file())

    def test_installer_restores_existing_files_permissions_and_new_paths(self):
        if os.geteuid()!=0:self.skipTest('root filesystem boundary test')
        spec=importlib.util.spec_from_file_location('installer',Path(__file__).with_name('install-update-migration.py'))
        installer=importlib.util.module_from_spec(spec);spec.loader.exec_module(installer)
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);old=root/'old-rule';old.write_text('original');old.chmod(0o440)
            new=root/'new-policy';private=root/'private'
            with mock.patch.object(installer.subprocess,'run'),self.assertRaises(RuntimeError):
                with installer.InstallTransaction() as transaction:
                    transaction.watch(old);old.write_text('changed');old.chmod(0o600)
                    installer.private_directory(private,transaction)
                    installer.immutable(new,b'candidate',transaction=transaction)
                    raise RuntimeError('failed installation')
            self.assertEqual(old.read_text(),'original');self.assertEqual(old.stat().st_mode&0o777,0o440)
            self.assertFalse(new.exists());self.assertFalse(private.exists())

    def test_orphan_cache_recovered_but_live_owner_and_mounts_protected(self):
        if os.geteuid()!=0:self.skipTest('root filesystem boundary test')
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);job=root/'task-99-fixture';job.mkdir(mode=0o700);(job/'cache').write_text('temporary')
            meta=job/'build-unit.json';meta.write_text(json.dumps({'unit':'ddyidc-migration-build-99','pid':99,'identity':'old'}));meta.chmod(0o600)
            state=subprocess.CompletedProcess([],0,'LoadState=loaded\nActiveState=active\n','')
            with mock.patch.object(m,'WORK',root),mock.patch.object(m,'process_identity',return_value='old'),self.assertRaises(m.Refused):m.cleanup_orphans()
            self.assertTrue(job.exists())
            with mock.patch.object(m,'WORK',root),mock.patch.object(m,'process_identity',return_value=None),mock.patch.object(m.subprocess,'run',return_value=state),mock.patch.object(m,'command') as stop:
                m.cleanup_orphans();stop.assert_called_once_with(['/usr/bin/systemctl','stop','ddyidc-migration-build-99.service'])
            self.assertFalse(job.exists())
            job.mkdir(mode=0o700)
            with mock.patch.object(m,'WORK',root),mock.patch.object(m.os.path,'ismount',return_value=True),self.assertRaises(m.Refused):m.cleanup_orphans()
            self.assertTrue(job.exists())

    def test_host_pin_is_not_silently_rotated_or_expanded(self):
        line=m.HOST+' ssh-ed25519 '+m.HOST_KEY+'\n'
        self.assertEqual(m.pinned_host_keys(line+'unrelated ssh-rsa fake\n'),line)
        with self.assertRaises(m.Refused):m.pinned_host_keys(m.HOST+' ssh-ed25519 unreviewed\n')

    def test_update_plan_refuses_symlink_and_oversize(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);outside=root/'other';outside.write_text('{}');plan=root/'pending.plan.json';plan.symlink_to(outside)
            with mock.patch.object(m,'RESULTS',root),self.assertRaises(OSError):m.read_plan()
            plan.unlink();plan.write_bytes(b'x'*65537)
            with mock.patch.object(m,'RESULTS',root),self.assertRaises(m.Refused):m.read_plan()

    def test_future_migration_cannot_ship_without_classification(self):
        spec=importlib.util.spec_from_file_location('gate',Path(__file__).resolve().parents[2]/'tools/architecture/check_automatic_migrations.py')
        gate=importlib.util.module_from_spec(spec);spec.loader.exec_module(gate)
        with tempfile.TemporaryDirectory() as folder:
            root,digest,rows=self.fixture(folder)
            sql=root/'internal/shared/migrations/sql';(sql/'001.sql').rename(sql/'055_fixture.sql')
            policy=root/'tools/architecture/automatic-migrations.json';policy.write_text(json.dumps({'055_fixture.sql':{'sha256':digest,'rollbackCompatible':True}}))
            gate.validate(root)
            (sql/'057_future.sql').write_text('CREATE TABLE future(id INT);')
            with self.assertRaises(ValueError):gate.validate(root)

    def test_bootstrap_and_handed_off_jobs_do_not_fabricate_results(self):
        with mock.patch.object(m,'OWNED_TASK','99'),mock.patch.object(m.sys,'argv',['worker','--bootstrap']),mock.patch.object(m,'atomic_result') as emit:
            m.report_public_failure(m.Refused('update in progress'));emit.assert_not_called()
        error=m.Refused('lease lost');error.handoff_owned=True
        with mock.patch.object(m,'OWNED_TASK','99'),mock.patch.object(m.sys,'argv',['worker']),mock.patch.object(m,'atomic_result') as emit:
            m.report_public_failure(error);emit.assert_not_called()

    def test_application_git_advance_is_unprivileged_and_fast_forward_only(self):
        with mock.patch.object(m,'command') as run,mock.patch.object(m.pwd,'getpwnam',return_value=object()):m.advance_workspace('a'*40,{},lambda:None)
        args,options=run.call_args
        self.assertIn('--ff-only',args[0]);self.assertNotIn('reset',args[0]);self.assertIsNotNone(options['identity'])

    def test_failure_messages_do_not_leak_command_details(self):
        self.assertNotIn('secret-payload',m.public_error(m.Refused('secret-payload')))
        self.assertIn('脏状态',m.public_error(m.Refused('dirty or changed migration history')))


if __name__=='__main__':unittest.main()
