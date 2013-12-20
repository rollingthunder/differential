package epp

import (
	"errors"
	. "github.com/rollingthunder/differential/ode"
	"github.com/rollingthunder/differential/ode/rk"
	"github.com/rollingthunder/differential/util"
	"math"
)

type peer struct {
	IntegratorInfo
	method PeerMethod
	peerCoefficients
}

type peerCoefficients struct {
	indexMinNode, indexMaxNode uint

	stepRatioMax float64

	// Error Model
	// p=order/2
	// ((1+a)^p-a^p)/est+a^p)^(1/p)-a
	errorModelA float64
	// a0 = a^p
	errorModelA0      float64
	errorModelWeights []float64

	c             []float64
	b, a0, cv, pv [][]float64
}

type integration struct {
	Config
	Statistics
	fOld, fNew, yOld, yNew, pa                                                 [][]float64
	errorFactors                                                               []float64
	tCurrent, stepRatioMin, stepRatio, stepEstimate, stepCurrent, stepPrevious float64
	n                                                                          uint
}

func (p *peer) Integrate(t, tEnd float64, yT []float64, cfg Config) (s Statistics, err error) {
	in := p.setupIntegration(t, tEnd, yT, cfg)

	in.tCurrent, in.stepPrevious = p.startupIntegration(&in, t)
	in.stepEstimate = in.stepPrevious // continue with stepsize stepPrevious

	// repeat until tend
	for in.tCurrent < (tEnd - in.AbsoluteTolerance) {
		if in.tCurrent+in.stepEstimate > tEnd {
			in.stepEstimate = tEnd - in.tCurrent
		}
		in.stepCurrent = in.stepEstimate
		in.StepCount++

		p.computeCoefficients(&in)

		p.computeStages(&in)

		p.computeEvaluations(&in)

		errorEstimate := p.computeErrorModel(&in)

		if errorEstimate > 1.0 {
			// reject step
			// decrease minimal stepsize ratio
			in.stepRatioMin = in.stepRatioMin * 0.2
			in.RejectedCount++

			// report failure
			if in.stepEstimate < in.MinStepSize {
				err = errors.New("step size too small")
				break
			}
		} else {
			// accept step
			in.stepRatioMin = 0.2

			// swap Y & F
			swap := in.yOld
			in.yOld = in.yNew
			in.yNew = swap

			swap = in.fOld
			in.fOld = in.fNew
			in.fNew = swap

			in.tCurrent += in.stepCurrent
			in.stepPrevious = in.stepCurrent
		}

		// failure, too many steps
		if in.StepCount > in.MaxStepCount {
			err = errors.New("maximum step count exceeded")
			break
		}
	}
	// output of last stage is the final output
	copy(yT, in.yOld[p.Stages-1])

	in.CurrentTime = in.tCurrent
	in.LastStepSize = in.stepPrevious
	in.NextStepSize = in.stepEstimate

	s = in.Statistics
	return
}

func (p *peer) setupIntegration(t, tEnd float64, yT []float64, c Config) (i integration) {
	i.Config = c

	// set default parameters if necessary
	if i.MaxStepSize <= 0.0 {
		i.MaxStepSize = tEnd - t
	}
	if i.MinStepSize <= 0.0 {
		i.MinStepSize = 1e-10
	}
	if i.MaxStepCount == 0 {
		i.MaxStepCount = 1000000
	}
	if i.AbsoluteTolerance <= 0.0 {
		i.AbsoluteTolerance = 1e-4
	}
	if i.RelativeTolerance <= 0.0 {
		i.RelativeTolerance = i.AbsoluteTolerance
	}

	// local variables
	i.n = uint(len(yT))

	// allocate temp matrices
	i.errorFactors = make([]float64, i.n)
	i.pa = util.MakeSquare(p.Stages)

	i.yNew = util.MakeRectangular(p.Stages, i.n)
	i.yOld = util.MakeRectangular(p.Stages, i.n)
	i.fNew = util.MakeRectangular(p.Stages, i.n)
	i.fOld = util.MakeRectangular(p.Stages, i.n)

	copy(i.yOld[p.indexMinNode], yT)

	i.stepRatioMin = 0.2

	return
}

func (p *peer) startupIntegration(in *integration, t0 float64) (tCurrent, stepRelative float64) {
	// startup with DOPRI
	dopri, err := rk.NewRK(rk.DoPri5)
	if err != nil {
		err = errors.New("error during startup: " + err.Error())
		return
	}

	in.Fcn(t0, in.yOld[p.indexMinNode], in.fOld[p.indexMinNode])
	in.EvaluationCount = 1

	// guess initial step size if unspecified
	in.stepEstimate = in.InitialStepSize
	if in.stepEstimate <= 0.0 {
		in.stepEstimate = EstimateStepSize(t0, in.yOld[p.indexMinNode], in.fOld[p.indexMinNode], &in.Config, p.Order)
	}

	copy(in.yOld[p.indexMaxNode], in.yOld[p.indexMinNode])
	tCurrent = t0

	//  higher accuracy for starting proc
	rkConfig := Config{
		InitialStepSize:   in.stepEstimate,
		RelativeTolerance: math.Max(1e-1*in.RelativeTolerance, 1e-14),
		AbsoluteTolerance: math.Max(1e-1*in.AbsoluteTolerance, 1e-14),
		OneStepOnly:       true,
		Fcn:               in.Fcn,
	}

	rkStat, err := dopri.Integrate(tCurrent, t0+in.stepEstimate, in.yOld[p.indexMaxNode], rkConfig)
	if err != nil {
		err = errors.New("error during startup: " + err.Error())
		return
	}
	tCurrent = rkStat.CurrentTime

	in.EvaluationCount += rkStat.EvaluationCount
	in.Fcn(tCurrent, in.yOld[p.indexMaxNode], in.fOld[p.indexMaxNode])
	in.EvaluationCount++

	// adjusted step size, relative to interval [0,1]:
	stepRelative = (tCurrent - t0) / (p.c[p.indexMaxNode] - p.c[p.indexMinNode])

	tBase := t0 - stepRelative*p.c[p.indexMinNode] // corresponds to node pc=0

	// startup procedure
	var stg uint
	for stg = 0; stg < p.Stages; stg++ {
		if stg != p.indexMinNode && stg != p.indexMaxNode {
			copy(in.yOld[stg], in.yOld[p.indexMinNode])
			rkConfig.InitialStepSize = stepRelative * (p.c[stg] - p.c[p.indexMinNode])
			tStage := tBase + stepRelative*p.c[stg]
			rkStat, err = dopri.Integrate(t0, tStage, in.yOld[stg], rkConfig)
			if err != nil {
				err = errors.New("error during startup: " + err.Error())
				return
			}
			in.Fcn(tStage, in.yOld[stg], in.fOld[stg])
			in.EvaluationCount += rkStat.EvaluationCount + 1
		}
	}

	tCurrent = tBase + stepRelative
	return
}

func (p *peer) computeCoefficients(in *integration) {
	in.stepRatio = in.stepCurrent / in.stepPrevious

	// COMPUTE COEFFS -> "Co" Prefix
	// stepPrevious*A row-wise
	// Loops: CoStages( CoA0, CoA1 )
	var stg, ic, id uint
	/*@; BEGIN(CoStages=Nest) @*/
	for stg = 0; stg < p.Stages; stg++ {
		/*@; BEGIN(CoA0=Nest) @*/
		for ic = 0; ic < p.Stages; ic++ {
			in.pa[stg][ic] = in.stepPrevious * p.a0[stg][ic]
		}

		stepStage := in.stepPrevious
		/*@; BEGIN(CoA1=Nest) @*/
		for ic = 0; ic < p.Stages; ic++ {
			stepStage *= in.stepRatio
			for id = 0; id < p.Stages; id++ {
				in.pa[stg][id] += p.cv[stg][ic] * stepStage * p.pv[ic][id]
			}
		}
	}
}

func (p *peer) computeStages(in *integration) {
	// STAGE SOLUTIONS -> "St" Prefix
	var stg, ic, id uint
	// Loops: StB
	for id = 0; id < in.n; id++ {
		for stg = 0; stg < p.Stages; stg++ {
			in.yNew[stg][id] = 0.0
			/*@; BEGIN(StB=Nest) @*/
			for ic = 0; ic < p.Stages; ic++ {
				in.yNew[stg][id] += p.b[stg][ic] * in.yOld[ic][id]
			}
		}
	}

	for id = 0; id < in.n; id++ {
		for stg = 0; stg < p.Stages; stg++ {
			for ic = 0; ic < p.Stages; ic++ {
				in.yNew[stg][id] += in.pa[stg][ic] * in.fOld[ic][id]
			}
		}
	}
}

func (p *peer) computeEvaluations(in *integration) {
	// FUNCTION EVALUATIONS
	// Fn=fcn(Yn)
	var stg uint
	// Candidate for Parallelisation
	for stg = 0; stg < p.Stages; stg++ {
		in.Fcn(in.tCurrent+in.stepEstimate*p.c[stg], in.yNew[stg], in.fNew[stg])
	}

	in.EvaluationCount += p.Stages
}

func (p *peer) computeErrorModel(in *integration) (errorEstimate float64) {
	var id, stg uint
	// compute error estimate with fNew:
	for id = 0; id < in.n; id++ {
		rc := 0.0
		for stg = 0; stg < p.Stages; stg++ {
			rc += p.errorModelWeights[stg] * in.fNew[stg][id]
		}
		in.errorFactors[id] = math.Pow(rc/(in.AbsoluteTolerance+in.RelativeTolerance*math.Abs(in.yOld[p.Stages-1][id])), 2.0)
	}

	// compute error quotient/20070803
	// step ratio from error model ((1+a)^p-a^p)/est+a^p)^(1/p)-a, p=order/2:
	errorRelative := 0.0
	for id = 0; id < in.n; id++ {
		errorRelative += in.errorFactors[id]
	}

	errorEstimate = in.stepEstimate*math.Sqrt(errorRelative/float64(in.n)) + 1e-8
	errorModelDenom := math.Pow(math.Pow(in.stepRatio, 2.0)+p.errorModelA, float64(p.Order)/2.0) - p.errorModelA0
	errorStepRatio := math.Pow(errorModelDenom/errorEstimate+p.errorModelA0, 2.0/float64(p.Order)) - p.errorModelA
	in.stepEstimate = in.stepPrevious * math.Max(in.stepRatioMin, math.Min(0.95*math.Sqrt(errorStepRatio), p.stepRatioMax)) // safety interval

	return
}
