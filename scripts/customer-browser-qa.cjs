// Starts only a disposable fixture database. Stop by creating the stop file below.
const fs=require('node:fs'),cp=require('node:child_process');
const cfg={};for(const line of fs.readFileSync('.env','utf8').split(/\r?\n/)){const m=line.match(/^([A-Z_]+)\s*=\s*(.*)$/);if(m)cfg[m[1]]=m[2].replace(/^(["'])(.*)\1$/,'$2')}
const url=new URL('postgres://127.0.0.1:15433/'+(cfg.POSTGRES_DB||'logaluxe'));url.username=cfg.POSTGRES_USER||'logaluxe';url.password=cfg.POSTGRES_PASSWORD;
const stop='E:/LogaLuxe-backups/customer-qa.stop';if(fs.existsSync(stop))fs.unlinkSync(stop);
const env={...process.env,SECURITY_TEST_DATABASE_URL:url.toString(),CUSTOMER_QA_STOP_FILE:stop,QA_STRIPE_KEY:cfg.STRIPE_SECRET_KEY||'',QA_PAYSTACK_KEY:cfg.PAYSTACK_SECRET_KEY||''};
const result=cp.spawnSync('go',['test','./internal/httpapi','-run','^TestCustomerBrowserFixture$','-v','-timeout','90m'],{env,stdio:'inherit'});process.exitCode=result.status??1;
