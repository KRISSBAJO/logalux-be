const fs=require('fs'),cp=require('child_process');
const env={...process.env};
for(const line of fs.readFileSync('.env','utf8').split(/\r?\n/)){const m=line.match(/^([A-Z_]+)\s*=\s*(.*)$/);if(m)env[m[1]]=m[2].replace(/^(["'])(.*)\1$/,'$2')}
// Pass credentials only via the child environment, never stdout or command-line arguments.
const u=new URL('postgres://127.0.0.1:15433/'+(env.POSTGRES_DB||'logaluxe'));u.username=env.POSTGRES_USER||'logaluxe';u.password=env.POSTGRES_PASSWORD;
env.SECURITY_TEST_DATABASE_URL=u.toString();
const result=cp.spawnSync('go',['test','./internal/httpapi','-run',process.argv[2]||'TestSecurity','-count=1','-v'],{env,stdio:'inherit'});process.exitCode=result.status??1;
