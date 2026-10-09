// Explicit migration stages; credentials stay in .env and never enter output.
const fs=require('node:fs'), cp=require('node:child_process'), path=require('node:path');
const cfg={}; for(const line of fs.readFileSync('.env','utf8').split(/\r?\n/)){const m=line.match(/^([A-Z_]+)=(.*)$/);if(m)cfg[m[1]]=m[2].replace(/^(["'])(.*)\1$/,'$2')}
const remote=new URL(cfg.RENVIQ_DATABASE_URL);
const env={...process.env,PGHOST:remote.hostname,PGPORT:remote.port||'5432',PGUSER:decodeURIComponent(remote.username),PGPASSWORD:decodeURIComponent(remote.password),PGDATABASE:remote.pathname.slice(1),PGSSLMODE:'verify-full',PGSSLROOTCERT:'/etc/ssl/certs/ca-certificates.crt',PGCONNECT_TIMEOUT:'15'};
function run(args,input){const r=cp.spawnSync('docker',args,{env,input,encoding:'utf8',maxBuffer:32*1024*1024});if(r.status!==0)throw Error((r.stderr||'Database operation failed').replaceAll(env.PGPASSWORD,'[REDACTED]'));return r.stdout}
const remoteArgs=['exec','-i',...['PGHOST','PGPORT','PGUSER','PGPASSWORD','PGDATABASE','PGSSLMODE','PGSSLROOTCERT','PGCONNECT_TIMEOUT'].flatMap(k=>['-e',k]),'logaluxe-db'];
function query(sql,isRemote=false){return run(isRemote?[...remoteArgs,'psql','-X','-q','-v','ON_ERROR_STOP=1','-At'] : ['exec','-i','logaluxe-db','psql','-X','-q','-U',cfg.POSTGRES_USER||'logaluxe','-d',cfg.POSTGRES_DB||'logaluxe','-v','ON_ERROR_STOP=1','-At'],sql)}
const counts=`select json_object_agg(name,n) from (select tablename name, (xpath('/row/n/text()',query_to_xml(format('select count(*) n from public.%I',tablename),false,true,'')))[1]::text::bigint n from pg_tables where schemaname='public') s;`;
const stage=process.argv[2]||'check';
if(stage==='check'){console.log(query("select json_build_object('connected',true,'version',current_setting('server_version'),'tls',(select ssl from pg_stat_ssl where pid=pg_backend_pid()),'tables',(select count(*) from pg_tables where schemaname='public'));",true));}
else if(stage==='progress'){console.log(query("select application_name,state,wait_event_type,wait_event,left(query,100) from pg_stat_activity where usename=current_user and pid<>pg_backend_pid();",true));}
else if(stage==='fingerprints'){
 const {file}=JSON.parse(fs.readFileSync('E:/LogaLuxe-backups/latest.json','utf8'));const tables=Object.keys(JSON.parse(fs.readFileSync(file+'.counts.json','utf8')));
 const sql='select json_object_agg(name,hash) from ('+tables.map(t=>`select '${t}' name, md5(coalesce(string_agg(md5(to_jsonb(t)::text), '' order by md5(to_jsonb(t)::text)), '')) hash from public."${t}" t`).join(' union all ')+') checks;';
 const a=JSON.parse(query(sql)),b=JSON.parse(query(sql,true));const changed=tables.filter(t=>a[t]!==b[t]);fs.writeFileSync('E:/LogaLuxe-backups/migration-fingerprints.json',JSON.stringify({tables:tables.length,changed,source:a,destination:b},null,2));console.log(JSON.stringify({tables:tables.length,changed}));if(changed.length)process.exitCode=1;
}
else if(stage==='backup'){
 const dir='E:/LogaLuxe-backups';fs.mkdirSync(dir,{recursive:true});
 const file=path.join(dir,'logaluxe-before-renviq-'+new Date().toISOString().replace(/[:.]/g,'-')+'.dump');
 const fd=fs.openSync(file,'wx'); const r=cp.spawnSync('docker',['exec','logaluxe-db','pg_dump','-U',cfg.POSTGRES_USER||'logaluxe','-d',cfg.POSTGRES_DB||'logaluxe','-Fc','--no-owner','--no-acl'],{stdio:['ignore',fd,'pipe'],encoding:'utf8'});fs.closeSync(fd);if(r.status!==0)throw Error(r.stderr);
 fs.writeFileSync(file+'.counts.json',query(counts));fs.writeFileSync(path.join(dir,'latest.json'),JSON.stringify({file,created:new Date().toISOString()}));console.log('Consistent backup created: '+file);
}else if(stage==='restore'){
 const n=Number(query("select count(*) from pg_tables where schemaname='public';",true).trim());if(n!==0)throw Error('Destination is not empty; refusing to overwrite it');
 const {file}=JSON.parse(fs.readFileSync('E:/LogaLuxe-backups/latest.json','utf8'));const fd=fs.openSync(file,'r');
 const r=cp.spawnSync('docker',[...remoteArgs,'pg_restore','--dbname',env.PGDATABASE,'--no-owner','--no-acl','--single-transaction','--exit-on-error'],{env,stdio:[fd,'pipe','pipe'],encoding:'utf8',maxBuffer:8*1024*1024});fs.closeSync(fd);if(r.status!==0)throw Error((r.stderr||'Restore failed').replaceAll(env.PGPASSWORD,'[REDACTED]'));console.log('Restore committed.');
}else if(stage==='verify'){
 const {file}=JSON.parse(fs.readFileSync('E:/LogaLuxe-backups/latest.json','utf8')); const before=JSON.parse(fs.readFileSync(file+'.counts.json','utf8')), after=JSON.parse(query(counts,true));
 const mismatches=Object.keys(before).filter(k=>before[k]!==after[k]); console.log(JSON.stringify({tables:Object.keys(before).length,mismatches,counts:{businesses:after.businesses,bookings:after.bookings,clients:after.clients,ledger:after.ledger,payments:after.payments}}));if(mismatches.length)process.exitCode=1;
}else if(stage==='audit'){
 const sql=fs.readFileSync('scripts/financial-audit.sql','utf8');const report=JSON.parse(query(sql,process.argv.includes('--remote')));fs.mkdirSync('E:/LogaLuxe-backups',{recursive:true});fs.writeFileSync('E:/LogaLuxe-backups/financial-audit.json',JSON.stringify(report,null,2));console.log(JSON.stringify(Object.fromEntries(Object.entries(report).map(([k,v])=>[k,Array.isArray(v)?v.length:v])),null,2));
}else throw Error('Unknown stage');
