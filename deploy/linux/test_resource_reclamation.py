import importlib.util
import json
import os
import pathlib
import socket
import subprocess
import tempfile
import time
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('reclamation',pathlib.Path(__file__).with_name('resource-reclamation.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class ResourceReclamationTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory(prefix='reclamation-unit-')
  self.root=pathlib.Path(self.tmp.name);self.jobs=self.root/'jobs';self.jobs.mkdir()
  self.results=self.root/'results';self.results.mkdir();self.front=self.root/'front';self.front.mkdir()
  self.patch=mock.patch.multiple(m,ROOT=self.root,JOBS=self.jobs,RESULTS=self.results,FRONTEND=self.front)
  self.patch.start();self.active_real=m.active_unit;self.unit=mock.patch.object(m,'active_unit',return_value=False);self.unit.start()
  self.records=[];self.sql_patch=mock.patch.object(m,'sql',return_value='');self.sql_patch.start()
 def tearDown(self):
  self.sql_patch.stop();self.unit.stop();self.patch.stop();self.tmp.cleanup()
 def job(self,n='1'):
  p=self.jobs/n;p.mkdir();(p/'source').mkdir();(p/'source'/'file').write_text('source');(p/'.cache').mkdir();(p/'.cache'/'cache').write_text('cache');return p
 def collect(self,tasks,dry=False):
  return m.collect(tasks,lambda:None,lambda *row:self.records.append(row),dry)
 def test_three_generations_and_failed_cancelled_jobs_leave_no_history(self):
  for n,status in [('1','success'),('2','failed'),('3','failed')]:
   self.job(n);summary=self.collect({n:{'status':status,'rollback':'success'}})
   self.assertEqual(summary['removed'],1);self.assertEqual(list(self.jobs.iterdir()),[])
  self.assertEqual(self.collect({'3':{'status':'success'}})['removed'],0)
 def test_running_unknown_rollback_and_active_build_protected(self):
  p=self.job();self.collect({'1':{'status':'running'}});self.assertTrue(p.exists())
  self.collect({'1':{'status':'failed','rollback':'unknown'}});self.assertTrue(p.exists())
  with mock.patch.object(m,'active_unit',return_value=True):self.collect({'1':{'status':'success'}})
  self.assertTrue(p.exists())
 def test_open_file_is_protected_until_closed(self):
  p=self.job()
  with (p/'source'/'file').open() as opened:
   self.assertEqual(self.collect({'1':{'status':'success'}})['blocked'],1)
  self.assertEqual(self.collect({'1':{'status':'success'}})['removed'],1)
 def test_preview_never_deletes_or_mutates_resource_ledger(self):
  p=self.job();s=self.collect({'1':{'status':'success'}},True)
  self.assertEqual(s['discovered'],1);self.assertEqual(s['removed'],0);self.assertTrue(p.exists());self.assertFalse(self.records)
 def test_symlink_does_not_delete_outside_or_follow_parent(self):
  outside=self.root/'database';outside.mkdir();(outside/'keep').write_text('data')
  (self.jobs/'1').symlink_to(outside,target_is_directory=True)
  self.assertEqual(self.collect({'1':{'status':'success'}})['failed'],1)
  self.assertTrue((outside/'keep').exists())
  with self.assertRaises(ValueError):m.safe_remove(self.root,self.root,lambda:None)
 def test_nested_link_unlinked_without_touching_database(self):
  p=self.job();outside=self.root/'db';outside.mkdir();(outside/'table').write_text('ledger')
  (p/'link').symlink_to(outside,target_is_directory=True)
  self.collect({'1':{'status':'success'}});self.assertEqual((outside/'table').read_text(),'ledger')
 def test_nested_mount_is_refused_before_any_deletion(self):
  p=self.job()
  with mock.patch.object(m,'mount_points',return_value={str(p/'source')}):
   with self.assertRaises(ValueError):m.safe_remove(p,self.jobs,lambda:None)
  self.assertTrue((p/'source'/'file').exists())
 def test_parent_symlink_and_root_delete_refused(self):
  parent=self.root/'link';parent.symlink_to(self.jobs,target_is_directory=True);self.job()
  with self.assertRaises((ValueError,OSError)):m.safe_remove(parent/'1',parent,lambda:None)
  with self.assertRaises(ValueError):m.safe_remove(self.root,self.root,lambda:None)
 def test_loss_of_lease_stops_deletion_and_is_retriable(self):
  p=self.job();count=[0]
  def guard():
   count[0]+=1
   if count[0]>3:raise RuntimeError('lease lost')
  s=m.collect({'1':{'status':'success'}},guard,lambda *x:None)
  self.assertEqual(s['failed'],1)
  self.collect({'1':{'status':'success'}});self.assertFalse(p.exists())
 def test_legacy_program_only_database_and_config_backups_untouched(self):
  b=self.root/'backups';b.mkdir();(b/'old-server').write_bytes(b'\x7fELFprogram')
  (b/'database.sql').write_text('precious');(b/'config.yaml').write_text('secret')
  self.assertEqual(self.collect({})['removed'],1)
  self.assertTrue((b/'database.sql').exists());self.assertTrue((b/'config.yaml').exists())
 def test_no_production_marker_means_no_database_or_deletion(self):
  with mock.patch.object(m.sys,'argv',['collector']),mock.patch.object(m,'MARKER',self.root/'absent'),mock.patch.object(m,'sql',side_effect=AssertionError('database touched')):
   self.assertEqual(m.main(),1)
 def test_binding_rejects_wrong_host_mode_and_nonproduction(self):
  marker=self.root/'binding.json'
  marker.write_text(json.dumps({'target':'wrong','machine_id':'wrong','version':1}));marker.chmod(0o600)
  with mock.patch.object(m,'MARKER',marker),mock.patch.object(m,'load_config',return_value={'DeploymentEnvironment':'production'}):
   with self.assertRaises(RuntimeError):m.private_marker()
  marker.chmod(0o644)
  with mock.patch.object(m,'MARKER',marker):
   with self.assertRaises(RuntimeError):m.private_marker()
  marker.chmod(0o600)
  marker.write_text(json.dumps({'target':'114.66.48.129','machine_id':pathlib.Path('/etc/machine-id').read_text().strip(),'version':1}))
  with mock.patch.object(m,'MARKER',marker),mock.patch.object(m,'load_config',return_value={'DeploymentEnvironment':'development'}),mock.patch.object(m,'sql',side_effect=AssertionError('nonproduction database touched')),mock.patch.object(m.sys,'argv',['collector']):
   self.assertEqual(m.main(),1)

 def test_bound_production_rejects_mysql_socket_mismatch(self):
  marker=self.root/'mysql-binding.json';marker.write_text(json.dumps({'target':'114.66.48.129','machine_id':pathlib.Path('/etc/machine-id').read_text().strip(),'version':1}));marker.chmod(0o600)
  cfg={'DeploymentEnvironment':'production','MySQL':{'Host':'127.0.0.1','Port':13306}}
  with mock.patch.object(m,'MARKER',marker),mock.patch.object(m,'load_config',return_value=cfg),mock.patch.object(m,'sql',return_value='3306'):
   with self.assertRaises(RuntimeError):m.private_marker()
   cfg['MySQL']['Port']=3306;m.private_marker()

 def test_systemd_missing_unit_is_distinct_from_unavailable_daemon(self):
  absent=subprocess.CompletedProcess([],1,'LoadState=not-found\nActiveState=inactive\n','unit not loaded')
  with mock.patch.object(m.subprocess,'run',return_value=absent):self.assertFalse(self.active_real('1'))
  unavailable=subprocess.CompletedProcess([],1,'','cannot connect to bus')
  with mock.patch.object(m.subprocess,'run',return_value=unavailable):
   with self.assertRaises(RuntimeError):self.active_real('1')

 def test_active_static_symlink_target_is_protected_without_open_files(self):
  releases=self.front/'.releases';releases.mkdir();current=releases/'1';current.mkdir();(current/'index.html').write_text('live')
  (self.front/'dist').symlink_to(current,target_is_directory=True)
  self.assertEqual(self.collect({'1':{'status':'success'}})['blocked'],1)
  self.assertTrue((current/'index.html').exists())
 def test_current_configuration_reference_is_protected(self):
  root=self.root/'backups';root.mkdir();cfg=root/'active-config.yaml';cfg.write_text('active')
  with mock.patch.object(m,'CONFIG_REFERENCES',m.configured_references({'Settings':{'File':str(cfg)}})),mock.patch.object(m,'sql',return_value=json.dumps({'relative':'active-config.yaml'})):
   self.assertEqual(self.collect({})['blocked'],1)
  self.assertTrue(cfg.exists())

 def test_untrusted_exception_text_is_not_written_to_audit(self):
  self.assertEqual(m.error_code(RuntimeError('private credential-like diagnostic')), 'RuntimeError')
  self.assertEqual(m.error_code(m.ReclamationError('update recovery pending')), 'update recovery pending')
 def test_registered_database_archive_is_protected(self):
  import tarfile
  root=self.root/'backups';root.mkdir();data=root/'restore.sql';data.write_text('ledger')
  archive=root/'snapshot.tar'
  with tarfile.open(archive,'w') as tar:tar.add(data,arcname='database/restore.sql')
  with mock.patch.object(m,'sql',return_value=json.dumps({'relative':'snapshot.tar'})):
   s=self.collect({})
  self.assertEqual(s['blocked'],1);self.assertTrue(archive.exists())
 def test_registered_non_database_copy_is_reclaimed(self):
  root=self.root/'backups';root.mkdir();(root/'old-config.yaml').write_text('obsolete duplicate')
  with mock.patch.object(m,'sql',return_value=json.dumps({'relative':'old-config.yaml'})):
   self.assertEqual(self.collect({})['removed'],1)
  self.assertFalse((root/'old-config.yaml').exists())

 def test_failed_delete_is_recorded_and_neighbour_is_reclaimed(self):
  self.job('1');self.job('2');real=m.safe_remove
  def remove(path,root,guard):
   if path.name=='1':raise PermissionError('denied')
   return real(path,root,guard)
  with mock.patch.object(m,'safe_remove',side_effect=remove):s=self.collect({'1':{'status':'success'},'2':{'status':'success'}})
  self.assertEqual((s['removed'],s['failed']),(1,1));self.assertTrue(any(row[3]=='failed' for row in self.records))

class RedisLeaseTests(unittest.TestCase):
 def test_real_redis_mutual_exclusion_owner_and_deadline(self):
  with tempfile.TemporaryDirectory(prefix='reclamation-redis-') as root:
   with socket.socket() as port:sockport=port.bind(('127.0.0.1',0)) or port.getsockname()[1]
   p=subprocess.Popen(['redis-server','--bind','127.0.0.1','--port',str(sockport),'--save','','--appendonly','no','--dir',root],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
   try:
    a=m.RedisLock({'Host':'127.0.0.1','Port':sockport});b=m.RedisLock({'Host':'127.0.0.1','Port':sockport})
    for _ in range(100):
     try:
      if a.command('PING')=='PONG':break
     except OSError:time.sleep(.02)
    self.assertTrue(a.acquire());self.assertFalse(b.acquire());b.release();a.check()
    a.release();self.assertTrue(b.acquire());b.started-=541
    with self.assertRaises(RuntimeError):b.check()
    b.release()
    # Dead collector locks can be recovered after process restart; updater tokens cannot.
    a.command('SET',m.LOCK_KEY,'reclamation:999999999:'+('0'*64)+':'+('a'*48))
    fresh=m.RedisLock({'Host':'127.0.0.1','Port':sockport});self.assertTrue(fresh.acquire());fresh.release()
    a.command('SET',m.LOCK_KEY,'123456789');self.assertFalse(fresh.acquire());a.command('DEL',m.LOCK_KEY)
   finally:p.terminate();p.wait(timeout=5)

if __name__=='__main__':unittest.main()
