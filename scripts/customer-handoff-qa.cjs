// Integration test against the disposable browser fixture only (ports 18081/3101).
const assert=require('node:assert/strict');
const api='http://127.0.0.1:18081/v1',web='http://localhost:3101';
async function post(url,body,headers={}){return fetch(url,{method:'POST',headers:{'Content-Type':'application/json',...headers},body:JSON.stringify(body)})}
(async()=>{
 const login=await post(api+'/auth/login',{email:'mobile-finish@example.test',password:'Local-Only-QA-2026-Test'});assert.equal(login.status,200);const {token}=await login.json();
 const handoff=await post(api+'/auth/web-handoff',{path:'/cart'},{Authorization:'Bearer '+token});assert.equal(handoff.status,200);const {url}=await handoff.json();const code=new URL(url).hash.slice(1);assert(code);
 const preview=await post(web+'/api/mobile-session/preview',{code},{Origin:web});assert.equal(preview.status,200);assert.equal((await preview.json()).first_name,'Mobile QA');
 const foreign=await post(web+'/api/mobile-session/exchange',{code},{Origin:'https://example.invalid'});assert.equal(foreign.status,403);
 const exchange=await post(web+'/api/mobile-session/exchange',{code},{Origin:web});assert.equal(exchange.status,200);const result=await exchange.json();assert.equal(result.next,'/cart');assert(!result.token,'bearer token leaked into browser response');
 const cookie=exchange.headers.get('set-cookie');assert(cookie.includes('HttpOnly'));assert(cookie.includes('SameSite=lax'));
 const account=await fetch(web+'/account',{headers:{Cookie:cookie.split(';')[0]}});assert.equal(account.status,200);assert((await account.text()).includes('Hello, '));
 const again=await post(web+'/api/mobile-session/exchange',{code},{Origin:web});assert.equal(again.status,410);
 console.log('PASS: native-to-web account exchange, httpOnly session, same-origin protection, authenticated account and one-use replay refusal');
})().catch(e=>{console.error(e);process.exitCode=1});
