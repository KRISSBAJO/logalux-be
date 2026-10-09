const fs=require('fs'),assert=require('assert/strict');
const config={};for(const line of fs.readFileSync('.env','utf8').split(/\r?\n/)){const m=line.match(/^([A-Z_]+)\s*=\s*(.*)$/);if(m)config[m[1]]=m[2].replace(/^(["'])(.*)\1$/,'$2')}
const base='http://127.0.0.1:18080';
async function get(path,admin=false){const r=await fetch(base+path,{headers:admin?{Authorization:'Bearer '+config.ADMIN_TOKEN}:{}});const data=await r.json();assert.equal(r.status,200,path+': '+(data.error||r.status));return data}
(async()=>{
 await get('/health');let booking,order;
 for(const [name,key,sort]of [['bookings','bookings','client_name'],['clients','clients','spent_cents'],['orders','orders','total_cents'],['payouts','payouts','amount_cents'],['audit','events','actor'],['support','tickets','priority']]){
  const d=await get('/v1/admin/'+name+'?per_page=1&page=2&sort='+sort+'&direction=desc',true);assert(d.pagination);assert(d[key].length<=1);assert.equal(d.pagination.per_page,1);
  console.log(name+': paginated, sorted, total '+d.pagination.total);
  if(name==='bookings')booking=d[key][0]?.id;if(name==='orders')order=d[key][0]?.id;
 }
 if(booking){const d=await get('/v1/bookings/'+booking);for(const k of ['client_name','notes','guest_name'])assert(!(k in d.booking));console.log('Public booking personal fields hidden')}
 if(order){const d=await get('/v1/orders/'+order);for(const k of ['customer_name','customer_phone','customer_email','address','user_id'])assert(!(k in d.order));console.log('Public order personal fields hidden')}
 for(const path of ['/','/b/nia','/journal','/shop','/admin']){const r=await fetch('http://localhost:3100'+path);assert.equal(r.status,200);console.log('Web '+path+': 200')}
 for(const name of ['bookings','clients','orders','payouts','audit','support']){
  const r=await fetch('http://localhost:3100/admin/'+name+'?page=2&per_page=25',{headers:{Cookie:'lx_session='+config.ADMIN_TOKEN}});const html=await r.text();assert.equal(r.status,200,'Admin page '+name);assert(html.includes('List pagination and sorting'),'Missing paging controls on '+name);assert(!html.includes('Application error'),'Render error on '+name);console.log('Admin '+name+': rendered with paging controls');
 }
 console.log('Read-only smoke checks passed.');
})().catch(e=>{console.error(e.message);process.exitCode=1});
