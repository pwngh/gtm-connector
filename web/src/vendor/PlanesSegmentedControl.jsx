// Adapted from Planes' free Segmented Control by Ashish Gogula.
// Source: https://useplanes.com/r/segmented-control.json
// See THIRD_PARTY_NOTICES.md for provenance and local changes.
import React, {forwardRef, useCallback, useEffect, useId, useLayoutEffect, useRef, useState} from 'react';
import {motion, useMotionValue, useReducedMotion, useSpring, useTransform, useVelocity} from 'motion/react';
import './planes.css';

const PILL_SPRING = {stiffness:380, damping:24, mass:.6};
const WIDTH_SPRING = {stiffness:400, damping:28, mass:.5};
const LABEL_SPRING = {type:'spring', stiffness:400, damping:28, mass:.5};
const PRESS_SPRING = {stiffness:600, damping:22, mass:.4};

/** Controlled view filter. Icons are React nodes; labels remain visible at every width. */
export default function PlanesSegmentedControl({options, value, onChange, label='View', className=''}) {
  const reduced = useReducedMotion();
  const controlId = useId();
  const containerRef = useRef(null), buttonRefs = useRef([]), firstMeasure = useRef(true);
  const activeIndex = Math.max(options.findIndex(option => option.value === value), 0);
  const rawX = useMotionValue(0), rawW = useMotionValue(0);
  const springX = useSpring(rawX, PILL_SPRING), springW = useSpring(rawW, WIDTH_SPRING);
  const velocity = useVelocity(springX);
  const stretch = useTransform(velocity, speed => Math.min(Math.abs(speed) * .04, 12));
  const pillWidth = useTransform([springW, stretch], ([width, extra]) => width + extra);
  const pillX = useTransform([springX, stretch], ([x, extra]) => Math.max(0, x - extra / 2));
  const pillBlur = useTransform(velocity, speed => `blur(${Math.min(Math.abs(speed) * .011, 5)}px)`);
  const [geometry, setGeometry] = useState(null);
  const [safari, setSafari] = useState(false);
  useEffect(() => {setSafari(/^((?!chrome|android).)*safari/i.test(navigator.userAgent));}, []);
  const optionsKey = options.map(option => `${option.value}:${option.label}`).join('|');

  const measure = useCallback(() => {
    const button = buttonRefs.current[activeIndex];
    if (!button || !containerRef.current) return;
    const x = button.offsetLeft, width = button.offsetWidth;
    if (firstMeasure.current) {
      rawX.jump(x); rawW.jump(width);
      springX.jump(x); springW.jump(width);
      firstMeasure.current = false;
    } else {
      rawX.set(x); rawW.set(width);
    }
    const separators = buttonRefs.current.slice(0, options.length - 1).filter(Boolean).map(node => node.offsetLeft + node.offsetWidth);
    setGeometry(previous => previous && previous.x === x && previous.width === width && previous.separators.join() === separators.join()
      ? previous : {x, width, separators});
  }, [activeIndex, options.length, rawX, rawW, springX, springW]);

  const measureRef = useRef(measure);
  useLayoutEffect(() => {measureRef.current = measure; measure();}, [measure, optionsKey]);
  useEffect(() => {
    const observer = new ResizeObserver(() => {measureRef.current();});
    if (containerRef.current) observer.observe(containerRef.current);
    buttonRefs.current.forEach(button => {if (button) observer.observe(button);});
    let live = true;
    document.fonts?.ready.then(() => {if (live) measureRef.current();});
    return () => {live = false; observer.disconnect();};
  }, [optionsKey]);

  function move(event, index) {
    if (!options.length || !['ArrowRight','ArrowLeft','ArrowDown','ArrowUp','Home','End'].includes(event.key)) return;
    event.preventDefault();
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1
      : (index + (['ArrowRight','ArrowDown'].includes(event.key) ? 1 : -1) + options.length) % options.length;
    onChange(options[next].value);
    buttonRefs.current[next]?.focus({preventScroll:true});
  }

  if (!options.length) return null;
  return <div ref={containerRef} className={`planes-segmented ${className}`} role="radiogroup" aria-label={label}>
    {geometry && (reduced
      ? <span aria-hidden="true" className="planes-segmented-pill" style={{left:geometry.x,width:geometry.width}}/>
      : <motion.span aria-hidden="true" className="planes-segmented-pill" style={{left:0,x:pillX,width:pillWidth,filter:safari?undefined:pillBlur}}/>)}
    {geometry?.separators.map((x,index) => <motion.span key={index} aria-hidden="true" className="planes-segmented-separator"
      animate={{opacity:(index===activeIndex-1||index===activeIndex) ? 0 : 1,scaleY:(index===activeIndex-1||index===activeIndex) ? .3 : 1}}
      transition={reduced?{duration:0}:{type:'spring',stiffness:400,damping:30}} style={{left:x}}/>)}
    {options.map((option,index) => <Segment key={option.value} ref={node => {buttonRefs.current[index]=node;}} option={option}
      id={`${controlId}-${option.value}`} selected={index===activeIndex} reduced={Boolean(reduced)}
      onClick={() => onChange(option.value)} onKeyDown={event => move(event,index)}/>)}
  </div>;
}

const Segment = forwardRef(function Segment({option, selected, reduced, id, onClick, onKeyDown}, ref) {
  const press = useMotionValue(1), pressedScale = useSpring(press, PRESS_SPRING);
  const release = () => press.set(1);
  return <motion.button ref={ref} type="button" id={id} role="radio" aria-checked={selected} tabIndex={selected?0:-1}
    className={`planes-segment ${selected?'is-selected':''}`} onClick={onClick} onKeyDown={onKeyDown}
    onPointerDown={() => {if(!reduced)press.set(.93);}} onPointerUp={release} onPointerLeave={release} onPointerCancel={release}
    style={{scale:reduced?1:pressedScale}}>
    <motion.span className="planes-segment-content" initial={false} animate={{opacity:selected?1:.82,scale:reduced?1:selected?1:.985}}
      transition={reduced?{duration:0}:LABEL_SPRING}>
      {option.icon&&<span className="planes-segment-icon" aria-hidden="true">{option.icon}</span>}
      <span>{option.label}</span>
    </motion.span>
  </motion.button>;
});
