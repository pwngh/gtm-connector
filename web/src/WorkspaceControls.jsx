import React from 'react';

export const Avatar=({name})=><span className="avatar" aria-hidden="true">{String(name ?? '').split(' ').map(x=>x[0]).slice(0,2).join('')}</span>;
export const Pill=({children,icon:Icon})=><span className="pill neutral">{Icon&&<Icon size={13}/>} {children}</span>;
