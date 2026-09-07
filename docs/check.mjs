import assert from 'node:assert/strict';
import {readFile,readdir,stat} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root=path.dirname(fileURLToPath(import.meta.url));
const site=JSON.parse(await readFile(path.join(root,'site.json'),'utf8'));
async function files(dir) {const result=[];for(const entry of await readdir(dir,{withFileTypes:true})){const file=path.join(dir,entry.name);assert.ok(!entry.isSymbolicLink(),'Docs must not contain symlinks');if(entry.isDirectory())result.push(...await files(file));else result.push(file);}return result.sort();}
assert.equal(site.schemaVersion,1);
assert.equal(new Set(site.navigation).size,site.navigation.length);
assert.equal(site.navigation[0],'index');
const expected=Object.keys(site.locales).flatMap(locale=>site.navigation.map(slug=>`content/${locale}/${slug}.md`)).sort();
assert.deepEqual((await files(path.join(root,'content'))).map(file=>path.relative(root,file)),expected);
for(const file of expected) {
  const text=await readFile(path.join(root,file),'utf8');
  for(const field of ['title','description','navTitle'])assert.match(text,new RegExp(`^${field}:\\s*\\S`, 'm'),`${file}: missing ${field}`);
  for(const [,raw] of text.matchAll(/(?:\]\(|(?:href|src)=")([^"\s)]+)/g)) {
    if(/^(?:[a-z]+:|\/|#)/i.test(raw))continue;
    const target=path.resolve(root,path.dirname(file),raw.split(/[?#]/)[0]);
    assert.ok(target.startsWith(root+path.sep),'Link escapes docs directory');
    assert.ok((await stat(target)).isFile(),`${file}: broken link ${raw}`);
  }
}
const provenance=JSON.parse(await readFile(path.join(root,'screenshots/source.json'),'utf8'));
for(const [name,hash] of Object.entries(provenance.images)) {
  const image=await readFile(path.join(root,'assets',name));
  assert.equal(createHash('sha256').update(image).digest('hex'),hash,`Screenshot provenance mismatch: ${name}`);
  assert.equal(image.subarray(1,4).toString(),'PNG');
  assert.equal(image.readUInt32BE(16),provenance.viewport.width);
  assert.equal(image.readUInt32BE(20),provenance.viewport.height);
}
console.log(`PASS: ${expected.length} chapters, relative links, translations, and ${Object.keys(provenance.images).length} original screenshots`);
