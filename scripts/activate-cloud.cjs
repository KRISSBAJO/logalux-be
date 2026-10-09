const fs=require('node:fs');
const file='.env';let text=fs.readFileSync(file,'utf8');const values={};
for(const line of text.split(/\r?\n/)){const m=line.match(/^([A-Z_]+)=(.*)$/);if(m)values[m[1]]=m[2].replace(/^(["'])(.*)\1$/,'$2')}
const verified=JSON.parse(fs.readFileSync('E:/LogaLuxe-backups/migration-fingerprints.json','utf8'));
if(verified.changed.length||verified.tables<90)throw Error('Migration fingerprints must match before activation');
const url=new URL(values.RENVIQ_DATABASE_URL);if(url.hostname!=='db.renviq.com'||url.pathname!=='/rkdb_0d69ccc75404'||/[•●*]/.test(decodeURIComponent(url.password)))throw Error('Unexpected migration destination');
if(!values.RELYKIT_API_KEY?.startsWith('rlk_live_'))throw Error('RelyKit key missing');
url.searchParams.set('sslmode','verify-full');
const backup='E:/LogaLuxe-backups/backend-before-cloud.env';if(!fs.existsSync(backup))fs.writeFileSync(backup,text,{flag:'wx'});
for(const[k,v]of Object.entries({DATABASE_URL:url.toString(),SEED:'false',MAIL_PROVIDER:'relykit',MAIL_FROM:'LogaLuxe <noreply@logaluxe.com>',RELYKIT_BASE_URL:'https://api.relykit.com'})){
 const line=k+"='"+v+"'";const re=new RegExp('^'+k+'=.*$','m');text=re.test(text)?text.replace(re,line):text.trimEnd()+'\n'+line+'\n';
}
fs.writeFileSync(file,text);console.log('Renviq database and RelyKit mail selected; secrets are kept in .env.');
