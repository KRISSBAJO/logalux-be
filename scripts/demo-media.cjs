// Install only generated demo media. Uses the normal admin/S3 APIs and preserves existing uploads.
// Run locally from logaluxe-be: node scripts/demo-media.cjs
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const folder = path.join(root, 'brand/generated-demo');
const assets = JSON.parse(fs.readFileSync(path.join(folder, 'manifest.json'), 'utf8'));
for(const asset of assets) {
 const target = path.join(folder,asset.key+'.png');
 if(!fs.existsSync(target)) fs.copyFileSync(asset.source,target);
}
const indexPath = path.join(folder, 'uploads.json');
const index = fs.existsSync(indexPath) ? JSON.parse(fs.readFileSync(indexPath, 'utf8')) : {};
const env = fs.readFileSync(path.join(root, 'logaluxe-be/.env'), 'utf8');
const setting = k => (env.match(new RegExp('^'+k+'=(.*)$','m'))?.[1] || '').trim().replace(/^(['"])(.*)\1$/, '$2');
const API = 'http://127.0.0.1:18080/v1';
let token;
async function request(method, route, body) {
 const form = body instanceof FormData;
 const r = await fetch(API+route, {method, headers:{...(form?{}:{'Content-Type':'application/json'}),...(token?{Authorization:'Bearer '+token}:{})},body:body===undefined?undefined:form?body:JSON.stringify(body),signal:AbortSignal.timeout(90000)});
 const data = await r.json();
 if(!r.ok) throw new Error(`${method} ${route}: ${r.status}: ${data.error || 'request failed'}`);
 return data;
}
async function upload(key, slot, ref, sort) {
 const asset = assets.find(a=>a.key===key);
 if(!asset) {console.log('Waiting for '+key); return null;}
 const recordKey = `${slot}/${ref}/${key}`;
 if(index[recordKey]) return index[recordKey];
 const form = new FormData();
 form.set('file',new Blob([fs.readFileSync(path.join(folder,key+'.png'))],{type:'image/png'}),key+'.png');
 form.set('slot',slot); form.set('ref',ref);
 const alt = 'AI-generated sample · '+asset.alt;
 form.set('alt',alt.slice(0,200)); form.set('caption_pos','none');
 const r = await request('POST','/admin/media',form);
 if(sort!==undefined) await request('PUT','/admin/media/'+r.id,{sort});
 index[recordKey]=r.id; fs.writeFileSync(indexPath,JSON.stringify(index,null,2)+'\n');
 console.log('Uploaded '+slot+'/'+ref+' · '+key);
 return r.id;
}
const merchants = {
 braids:['ada','nia','peachtree','tiwa'],barber:['barberloft','bayoufade','bealebarbers','freedomway'],
 nails:['crenshawnails'],skin:['glow','glowup','harlemglow'],spa:['mnm'],makeup:['gardencitymakeup'],lashes:['wuselashes']
};
const articleFor = {braids:'knotless-braids-what-to-ask-for',barber:'how-to-choose-a-barber-and-describe-a-fade',nails:'gel-or-acrylic-nails-which-to-book',skin:'skin-care-for-humid-lagos',makeup:'bridal-makeup-timing',lashes:'lash-extension-care'};
const articleFields = ['title','slug','dek','body_md','cover_media_id','cover_alt','category','tags','author_name','author_role','author_media_id','country','featured','sort','related_category','cta_text','seo_title','seo_description'];
(async()=>{try {
 token=(await request('POST','/admin/login',{email:setting('ADMIN_EMAIL'),password:setting('ADMIN_PASSWORD')})).token;
 for(const [category,slugs] of Object.entries(merchants)) {
  const gallery=['merchant-'+category,category==='spa'?'merchant-treatment-room':'article-'+articleFor[category],['skin','lashes'].includes(category)?'merchant-treatment-room':'merchant-interior'];
  for(const slug of slugs) for(let i=0;i<gallery.length;i++) await upload(gallery[i],'business',slug,-30+i);
 }
 for(const category of ['hair','braids','barber','nails','lashes','skin','makeup','spa']) await upload(category==='hair'?'article-silk-press-care':'merchant-'+category,'category',category,-30);
 for(const asset of assets.filter(a=>a.key.startsWith('product-'))) await upload(asset.key,'product',asset.key.slice(8),-30);
 const list=await request('GET','/admin/journal');
 for(const asset of assets.filter(a=>a.key.startsWith('article-'))) {
  const slug=asset.key.slice(8), summary=list.articles.find(a=>a.slug===slug);
  if(!summary) continue;
  const id=await upload(asset.key,'article',slug,-30);
  if(!id)continue;
  const {article}=await request('GET','/admin/journal/'+summary.id);
  const body=Object.fromEntries(articleFields.map(k=>[k,article[k]]));
  body.cover_media_id=id; body.cover_alt=('AI-generated sample · '+asset.alt).slice(0,200);
  await request('PUT','/admin/journal/'+summary.id,body);
  console.log('Updated article cover: '+slug);
 }
 console.log('Saved upload index. Existing images and seeded business records preserved.');
 }finally{if(token)await request('POST','/admin/logout');}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
